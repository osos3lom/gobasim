package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	googleProto "google.golang.org/protobuf/proto"

	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/agentcfg"
	"sawt-go/internal/audio"
	"sawt-go/internal/erp"
	"sawt-go/internal/monitor"
	"sawt-go/internal/ratelimit"
	"sawt-go/internal/speech"
	"sawt-go/internal/trace"
	"sawt-go/internal/voicenotes"
	waClient "sawt-go/internal/whatsmeow"
	"sawt-go/internal/workflow"
	"sawt-go/web"
)

const MessageProcessingTimeout = 180 * time.Second

// InboundLimiter throttles inbound messages to 8 msgs/min per chat.
var InboundLimiter = ratelimit.New(8, time.Minute)

// ClientLIDResolver wraps a whatsmeow client to resolve LID users to phone numbers.
type ClientLIDResolver struct {
	Client *whatsmeow.Client
}

func (r ClientLIDResolver) ResolvePhoneForLID(ctx context.Context, lidUser string) (string, bool) {
	return waClient.ResolvePhoneForLIDForClient(ctx, r.Client, lidUser)
}

// NewContactParams builds initial WaContact parameters for a newly discovered WhatsApp user.
func NewContactParams(chatID, pushName string, bp web.BlueprintDefaults) database.CreateOrUpdateWaContactParams {
	name := strings.TrimSpace(pushName)
	if name == "" {
		name = waDisplayPhone(chatID)
	}

	var agentID *string
	if bp.DefaultAgentID != "" {
		agentID = &bp.DefaultAgentID
	}
	var prompt *string
	if bp.DefaultPromptOverride != "" {
		prompt = &bp.DefaultPromptOverride
	}

	return database.CreateOrUpdateWaContactParams{
		ChatID:         chatID,
		Name:           name,
		Enabled:        bp.AutoEnable,
		AgentID:        agentID,
		PromptOverride: prompt,
	}
}

func waDisplayPhone(chatID string) string {
	parts := strings.Split(chatID, "@")
	if len(parts) == 0 {
		return chatID
	}
	return "+" + parts[0]
}

// HandleIncomingMessage is the main pipeline handler for incoming WhatsApp messages.
func HandleIncomingMessage(
	ctx context.Context,
	cfg *config.Config,
	client *whatsmeow.Client,
	evt *events.Message,
	queries *database.Queries,
	sttOrch *speech.STTOrchestrator,
	ttsOrch *speech.TTSOrchestrator,
	erpClient *erp.Client,
	wfEngine *workflow.WorkflowEngine,
	voiceStore *voicenotes.Store,
) {
	if evt.Info.IsFromMe {
		return
	}
	if evt.Info.Chat.String() == "status@broadcast" {
		return
	}
	if evt.Info.IsGroup {
		trace.Logf(ctx, "Inbound: Skipping group message %s from %s", evt.Info.ID, evt.Info.Chat.String())
		return
	}

	ctx = trace.With(ctx, evt.Info.ID)

	ctx, cancel := context.WithTimeout(ctx, MessageProcessingTimeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			monitor.ReportPanic("HandleIncomingMessage", r)
			SendTextReply(ctx, client, evt.Info.Chat, "حدث خطأ غير متوقع أثناء معالجة طلبك.")
		}
	}()

	trace.Logf(ctx, "Inbound: Received message from %s", evt.Info.Chat.String())

	if _, err := queries.MarkMessageProcessed(ctx, evt.Info.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			trace.Logf(ctx, "Inbound: duplicate delivery of %s — already processed, skipping", evt.Info.ID)
			return
		}
		trace.Logf(ctx, "Inbound: dedup check failed for %s (proceeding): %v", evt.Info.ID, err)
	}

	if allowed, count := InboundLimiter.Allow(evt.Info.Chat.String()); !allowed {
		if count == 9 {
			SendTextReply(ctx, client, evt.Info.Chat, "أرسلت رسائل كثيرة خلال وقت قصير. يرجى الانتظار قليلاً ثم المحاولة مرة أخرى.")
		}
		trace.Logf(ctx, "Inbound: Rate limit exceeded for %s (%d msgs/min), dropping message", evt.Info.Chat.String(), count)
		return
	}

	contact, err := queries.GetWaContact(ctx, evt.Info.Chat.String())
	if err != nil {
		var bp web.BlueprintDefaults
		if settings, sErr := queries.GetSettings(ctx); sErr == nil && len(settings.BotConfig) > 0 {
			_ = json.Unmarshal(settings.BotConfig, &bp)
		}
		contact, err = queries.CreateOrUpdateWaContact(ctx, NewContactParams(evt.Info.Chat.String(), evt.Info.PushName, bp))
		if err != nil {
			trace.Logf(ctx, "Inbound: Warning: Failed to auto-create contact %s: %v", evt.Info.Chat.String(), err)
			return
		}
	}

	var text string
	if evt.Message.Conversation != nil {
		text = *evt.Message.Conversation
	} else if evt.Message.ExtendedTextMessage != nil && evt.Message.ExtendedTextMessage.Text != nil {
		text = *evt.Message.ExtendedTextMessage.Text
	} else if evt.Message.ImageMessage != nil && evt.Message.ImageMessage.Caption != nil {
		text = *evt.Message.ImageMessage.Caption
	} else if evt.Message.VideoMessage != nil && evt.Message.VideoMessage.Caption != nil {
		text = *evt.Message.VideoMessage.Caption
	}

	var identity *erp.Identity
	linkResult, lErr := erp.ResolveAndPersistContactIdentity(ctx, erpClient, queries, evt.Info.Chat.String(), contact.ErpPhoneOverride, ClientLIDResolver{Client: client})
	if lErr != nil {
		trace.Logf(ctx, "Inbound: Identity resolution warning for %s: %v", evt.Info.Chat.String(), lErr)
	} else if linkResult != nil {
		identity = linkResult.Identity
	}

	if !contact.Enabled {
		trimmedLower := strings.ToLower(strings.TrimSpace(text))
		isAffirmative := trimmedLower == "نعم" || trimmedLower == "yes" || trimmedLower == "تأكيد" || trimmedLower == "confirm" || trimmedLower == "1"

		if identity != nil {
			if isAffirmative {
				updated, uErr := queries.CreateOrUpdateWaContact(ctx, database.CreateOrUpdateWaContactParams{
					ChatID:         contact.ChatID,
					Name:           identity.DisplayName,
					Enabled:        true,
					AgentID:        contact.AgentID,
					PromptOverride: contact.PromptOverride,
				})
				if uErr == nil {
					contact = updated
					SendTextReply(ctx, client, evt.Info.Chat, fmt.Sprintf("تم ربط حسابك بنجاح بصفة (%s)! أهلاً بك في مساعد صوت، كيف يمكنني مساعدتك اليوم؟", identity.Role))
					trace.Logf(ctx, "Inbound: Contact %s confirmed identity link as %s (%s)", contact.ChatID, identity.DisplayName, identity.Role)
				}
			} else {
				promptMsg := fmt.Sprintf("مرحباً %s! عثرنا على حسابك في النظام بصفتك (%s). هل ترغب في ربط هذا الرقم بحسابك والتواصل مع مساعد صوت؟ أجب بـ 'نعم' للتأكيد.", identity.DisplayName, identity.Role)
				SendTextReply(ctx, client, evt.Info.Chat, promptMsg)
				trace.Logf(ctx, "Inbound: Sent identity confirmation prompt to %s for ERP user %s", contact.ChatID, identity.DisplayName)
				return
			}
		} else {
			SendTextReply(ctx, client, evt.Info.Chat, "أهلاً بك! رقمك غير مسجل في نظام مشالية بعد. يرجى تزويد المسؤول برقم جوالك أو تحديث بياناتك في النظام لربط الحساب.")
			trace.Logf(ctx, "Inbound: Discarding message from unlinked contact %s (%s)", contact.Name, contact.ChatID)
			return
		}
	}

	var incomingAudio []byte
	isAudio := evt.Message.AudioMessage != nil
	var sttProvider, ttsProvider string
	var sttMs, llmMs, ttsMs int32

	if isAudio {
		trace.Logf(ctx, "Inbound: Message contains audio. Downloading...")
		incomingAudio, err = client.Download(ctx, evt.Message.AudioMessage)
		if err != nil {
			trace.Logf(ctx, "Inbound: Failed to download audio: %v", err)
			errorMsg := "عذراً، لم أتمكن من تحميل الرسالة الصوتية."
			SendTextReply(ctx, client, evt.Info.Chat, errorMsg)
			_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
				ID:        evt.Info.ID,
				ChatID:    evt.Info.Chat.String(),
				Direction: "in",
				Sender:    "contact",
				MsgType:   "voice",
				Content:   "[Failed to download audio message]",
				Status:    "failed",
			})
			_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
				ID:        evt.Info.ID + "-out",
				ChatID:    evt.Info.Chat.String(),
				Direction: "out",
				Sender:    "bot",
				MsgType:   "text",
				Content:   errorMsg,
				Status:    "sent",
			})
			return
		}

		voiceStore.Save(ctx, voicenotes.Meta{
			MessageID:       evt.Info.ID,
			ChatID:          evt.Info.Chat.String(),
			Direction:       "in",
			Sender:          strings.Split(evt.Info.Chat.String(), "@")[0],
			Receiver:        "sawt",
			DurationSeconds: int32(evt.Message.AudioMessage.GetSeconds()),
			Timestamp:       evt.Info.Timestamp,
		}, incomingAudio)

		trace.Logf(ctx, "Inbound: Transcoding OGG/Opus to WAV...")
		wavBytes, err := audio.OggToWav(incomingAudio)
		if err != nil {
			trace.Logf(ctx, "Inbound: Audio transcoding failed: %v", err)
			errorMsg := "عذراً، فشل معالجة الملف الصوتي."
			SendTextReply(ctx, client, evt.Info.Chat, errorMsg)
			_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
				ID:        evt.Info.ID,
				ChatID:    evt.Info.Chat.String(),
				Direction: "in",
				Sender:    "contact",
				MsgType:   "voice",
				Content:   "[Failed to transcode audio message]",
				Status:    "failed",
			})
			_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
				ID:        evt.Info.ID + "-out",
				ChatID:    evt.Info.Chat.String(),
				Direction: "out",
				Sender:    "bot",
				MsgType:   "text",
				Content:   errorMsg,
				Status:    "sent",
			})
			return
		}

		sttStart := time.Now()
		var lang = "ar"
		text, sttProvider, err = sttOrch.Transcribe(ctx, wavBytes, lang)
		sttMs = int32(time.Since(sttStart).Milliseconds())

		if err != nil {
			monitor.ReportError(ctx, "stt", err)
			errorMsg := "عذراً، لم أتمكن من تحويل الصوت إلى نص."
			SendTextReply(ctx, client, evt.Info.Chat, errorMsg)
			_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
				ID:        evt.Info.ID,
				ChatID:    evt.Info.Chat.String(),
				Direction: "in",
				Sender:    "contact",
				MsgType:   "voice",
				Content:   "[Failed to transcribe audio message]",
				Status:    "failed",
			})
			_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
				ID:        evt.Info.ID + "-out",
				ChatID:    evt.Info.Chat.String(),
				Direction: "out",
				Sender:    "bot",
				MsgType:   "text",
				Content:   errorMsg,
				Status:    "sent",
			})
			return
		}

		trace.Logf(ctx, "Inbound: Audio transcribed in %dms via %s: '%s'", sttMs, sttProvider, text)

		_ = queries.CreateSttHistory(ctx, database.CreateSttHistoryParams{
			ID:         evt.Info.ID,
			Transcript: text,
			Filename:   "voice.ogg",
			DurationMs: sttMs,
			Language:   lang,
		})
	}

	if text == "" {
		return
	}

	inMsgType := "text"
	if isAudio {
		inMsgType = "voice"
	}
	_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
		ID:        evt.Info.ID + "-in",
		ChatID:    evt.Info.Chat.String(),
		Direction: "in",
		Sender:    "contact",
		MsgType:   inMsgType,
		Content:   text,
		Status:    "delivered",
	})

	if erp.ApplyDefaultOrg(identity, cfg.DefaultOrgID) {
		trace.Logf(ctx, "Inbound: applied DEFAULT_ORG_ID=%q for privileged orgless actor %s", cfg.DefaultOrgID, identity.UID)
	}

	summary, history, err := wfEngine.LoadConversation(ctx, evt.Info.Chat.String())
	if err != nil {
		trace.Logf(ctx, "Inbound: Warning: failed to load conversation history: %v", err)
	}

	messagesHistory := append(history, workflow.Message{
		Role:    "user",
		Content: text,
	})

	state := &workflow.State{
		Messages:      messagesHistory,
		ActorIdentity: identity,
		ChatID:        evt.Info.Chat.String(),
		Summary:       summary,
	}

	llmStart := time.Now()
	err = wfEngine.Execute(ctx, state)
	llmMs = int32(time.Since(llmStart).Milliseconds())

	if err != nil {
		monitor.ReportError(ctx, "workflow", err)
		errorMsg := "حدث خطأ أثناء معالجة طلبك."
		SendTextReply(ctx, client, evt.Info.Chat, errorMsg)
		_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
			ID:        evt.Info.ID + "-out",
			ChatID:    evt.Info.Chat.String(),
			Direction: "out",
			Sender:    "bot",
			MsgType:   "text",
			Content:   errorMsg,
			Status:    "sent",
		})
		return
	}

	replyText := state.FinalReply
	trace.Logf(ctx, "Inbound: Agent replied in %dms: '%s'", llmMs, replyText)

	wfEngine.SaveTurns(ctx, evt.Info.Chat.String(), text, replyText)

	var replyAudio []byte
	var rawWav []byte
	if isAudio && replyText != "" {
		trace.Logf(ctx, "Outbound: Synthesizing reply text to speech...")
		ttsStart := time.Now()
		voice := wfEngine.ResolveTTS(ctx, evt.Info.Chat.String())
		ttsCtx := agentcfg.WithVoice(ctx, voice)
		rawWav, ttsProvider, err = ttsOrch.Synthesize(ttsCtx, replyText, voice.LanguageCode)
		ttsMs = int32(time.Since(ttsStart).Milliseconds())

		if err != nil {
			trace.Logf(ctx, "Outbound: TTS Synthesis failed: %v", err)
		} else {
			trace.Logf(ctx, "Outbound: Transcoding WAV to OGG/Opus...")
			replyAudio, err = audio.WavToOpus(rawWav)
			if err != nil {
				trace.Logf(ctx, "Outbound: Audio transcoding failed: %v", err)
				replyAudio = nil
			} else {
				_ = queries.CreateTtsHistory(ctx, database.CreateTtsHistoryParams{
					ID:         evt.Info.ID + "-reply",
					Text:       replyText,
					Model:      ttsProvider,
					Speed:      1.0,
					DurationMs: ttsMs,
					SizeBytes:  int32(len(replyAudio)),
				})

				voiceStore.Save(ctx, voicenotes.Meta{
					MessageID: evt.Info.ID + "-reply",
					ChatID:    evt.Info.Chat.String(),
					Direction: "out",
					Sender:    "sawt",
					Receiver:  strings.Split(evt.Info.Chat.String(), "@")[0],
					Timestamp: time.Now(),
				}, replyAudio)
			}
		}
	}

	totalMs := sttMs + llmMs + ttsMs
	var activityStatus = "ok"
	var activityError *string

	if len(replyAudio) > 0 {
		trace.Logf(ctx, "Outbound: Uploading audio response to WhatsApp...")
		resp, err := client.Upload(context.Background(), replyAudio, whatsmeow.MediaAudio)
		if err != nil {
			trace.Logf(ctx, "Outbound: Failed to upload audio: %v, falling back to text", err)
			SendTextReply(ctx, client, evt.Info.Chat, replyText)

			errStr := err.Error()
			activityStatus = "failed"
			activityError = &errStr
		} else {
			durationSeconds := len(replyAudio) / 4000
			if durationSeconds == 0 {
				durationSeconds = 1
			}

			wave := audio.GenerateWaveform(rawWav)

			audioMsg := &waE2E.Message{
				AudioMessage: &waE2E.AudioMessage{
					URL:           googleProto.String(resp.URL),
					DirectPath:    googleProto.String(resp.DirectPath),
					MediaKey:      resp.MediaKey,
					Mimetype:      googleProto.String("audio/ogg; codecs=opus"),
					PTT:           googleProto.Bool(true),
					FileLength:    googleProto.Uint64(uint64(len(replyAudio))),
					FileSHA256:    resp.FileSHA256,
					FileEncSHA256: resp.FileEncSHA256,
					Seconds:       googleProto.Uint32(uint32(durationSeconds)),
					Waveform:      wave,
				},
			}
			_, err = client.SendMessage(context.Background(), evt.Info.Chat, audioMsg)
			if err != nil {
				monitor.ReportError(ctx, "whatsapp-send", err)
				errStr := err.Error()
				activityStatus = "failed"
				activityError = &errStr
			}
		}
	} else if replyText != "" {
		SendTextReply(ctx, client, evt.Info.Chat, replyText)
	}

	if replyText != "" {
		outMsgType := "text"
		if len(replyAudio) > 0 {
			outMsgType = "voice"
		}
		outStatus := "sent"
		if activityStatus != "ok" {
			outStatus = "failed"
		}
		_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
			ID:        evt.Info.ID + "-out",
			ChatID:    evt.Info.Chat.String(),
			Direction: "out",
			Sender:    "bot",
			MsgType:   outMsgType,
			Content:   replyText,
			Status:    outStatus,
		})
	}

	toolCallsBytes, _ := json.Marshal(state.ToolResults)

	var assignedAgentID *string
	contact, err = queries.GetWaContact(ctx, evt.Info.Chat.String())
	if err == nil && contact.AgentID != nil {
		assignedAgentID = contact.AgentID
	}

	llmModelPtr := &cfg.NimModel

	var ttsProvPtr *string
	if ttsProvider != "" {
		ttsProvPtr = &ttsProvider
	}

	_ = queries.CreateWaActivity(ctx, database.CreateWaActivityParams{
		ID:          evt.Info.ID,
		ChatID:      evt.Info.Chat.String(),
		ContactName: evt.Info.PushName,
		Direction:   "in",
		MsgType:     "ptt",
		Transcript:  text,
		Reply:       replyText,
		Language:    "ar",
		AgentID:     assignedAgentID,
		LlmModel:    llmModelPtr,
		TtsModel:    ttsProvPtr,
		ToolCalls:   toolCallsBytes,
		SttMs:       sttMs,
		LlmMs:       llmMs,
		TtsMs:       ttsMs,
		TotalMs:     totalMs,
		Status:      activityStatus,
		Error:       activityError,
	})
}

// SendTextReply sends a text reply over WhatsApp.
func SendTextReply(ctx context.Context, client *whatsmeow.Client, chat types.JID, text string) {
	textMsg := &waE2E.Message{
		Conversation: googleProto.String(text),
	}
	_, err := client.SendMessage(context.Background(), chat, textMsg)
	if err != nil {
		trace.Logf(ctx, "Outbound: Failed to send text reply: %v", err)
	}
}
