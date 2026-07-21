// Command aicheck exercises the live AI speech providers (Hugging Face
// STT/TTS, Google REST STT/TTS via GCP_API_KEY, Google ADC STT/TTS via
// GOOGLE_APPLICATION_CREDENTIALS, and GCS voice-note upload) directly
// against the real external services. Like cmd/erpcheck, it is a manual,
// narrative verification tool a human runs locally with real credentials
// and reads PASS/FAIL/SKIP from stdout — it never fails the build and never
// prints raw key material.
//
//	go run ./cmd/aicheck                          # every check whose creds are present in env/.env
//	go run ./cmd/aicheck -only hf,google-adc,gcs   # restrict to specific providers
//	go run ./cmd/aicheck -edge-cases               # also run the edge-case checklist
//	go run ./cmd/aicheck -bucket my-qa-bucket      # override VOICE_STORAGE_BUCKET for the GCS check
package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"sawt-go/config"
	"sawt-go/internal/speech/providers"
	"sawt-go/internal/voicenotes"
)

// checkResult is one PASS/FAIL/SKIP line of output.
type checkResult struct {
	Provider string
	Check    string
	Pass     bool
	Skip     bool
	Detail   string
	Elapsed  time.Duration
}

func main() {
	envFile := flag.String("env", ".env", "env file with HF_API_KEY / GCP_API_KEY / GOOGLE_APPLICATION_CREDENTIALS / VOICE_STORAGE_BUCKET")
	only := flag.String("only", "", "comma-separated subset of: hf,google,google-adc,gcs (default: all)")
	edgeCases := flag.Bool("edge-cases", false, "also run the QA edge-case checklist against real services")
	bucket := flag.String("bucket", "", "override VOICE_STORAGE_BUCKET for the GCS check")
	flag.Parse()

	loadDotEnv(*envFile)
	cfg := config.LoadConfig()

	wanted := parseOnly(*only)
	voiceBucket := cfg.VoiceStorageBucket
	if *bucket != "" {
		voiceBucket = *bucket
	}

	fmt.Println("aicheck — live AI speech provider verification")
	fmt.Printf("HF_API_KEY: %s   GCP_API_KEY: %s   GOOGLE_APPLICATION_CREDENTIALS: %s   VOICE_STORAGE_BUCKET: %s\n\n",
		presence(cfg.HfAPIKey), presence(cfg.GcpApiKey), presence(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")), presence(voiceBucket))

	var results []checkResult
	ctx := context.Background()

	if wanted("hf") {
		results = append(results, checkHuggingFaceSTT(ctx, cfg.HfAPIKey))
		results = append(results, checkHuggingFaceTTS(ctx))
	}
	if wanted("google") {
		results = append(results, checkGoogleRESTSTT(ctx, cfg.GcpApiKey))
		results = append(results, checkGoogleRESTTTS(ctx, cfg.GcpApiKey))
	}
	if wanted("google-adc") {
		results = append(results, checkGoogleADCSTT(ctx))
		results = append(results, checkGoogleADCTTS(ctx))
	}
	if wanted("gcs") {
		results = append(results, checkGCSRoundTrip(ctx, voiceBucket))
	}

	for _, r := range results {
		printResult(r)
	}

	if *edgeCases {
		fmt.Println("\n--- edge-case checklist ---")
		for _, r := range runEdgeCases(ctx, cfg, voiceBucket) {
			printResult(r)
		}
	}

	fail := 0
	for _, r := range results {
		if !r.Pass && !r.Skip {
			fail++
		}
	}
	if fail > 0 {
		fmt.Printf("\n%d check(s) FAILED\n", fail)
		os.Exit(1)
	}
	fmt.Println("\ndone")
}

func parseOnly(only string) func(name string) bool {
	if strings.TrimSpace(only) == "" {
		return func(string) bool { return true }
	}
	set := map[string]bool{}
	for _, p := range strings.Split(only, ",") {
		set[strings.TrimSpace(p)] = true
	}
	return func(name string) bool { return set[name] }
}

func presence(v string) string {
	if v == "" {
		return "NOT SET"
	}
	return "set"
}

func printResult(r checkResult) {
	status := "PASS"
	switch {
	case r.Skip:
		status = "SKIP"
	case !r.Pass:
		status = "FAIL"
	}
	fmt.Printf("[%s] %-12s %-28s (%s) %s\n", status, r.Provider, r.Check, r.Elapsed.Round(time.Millisecond), r.Detail)
}

func timeIt(fn func() error) (time.Duration, error) {
	start := time.Now()
	err := fn()
	return time.Since(start), err
}

// ── checks ──────────────────────────────────────────────────────────────

func checkHuggingFaceSTT(ctx context.Context, apiKey string) checkResult {
	r := checkResult{Provider: "hf", Check: "stt-transcribe"}
	if apiKey == "" {
		r.Skip = true
		r.Detail = "HF_API_KEY not set"
		return r
	}
	wav, err := os.ReadFile("internal/speech/providers/testdata/tiny_16k_mono.wav")
	if err != nil {
		r.Detail = fmt.Sprintf("could not load fixture: %v", err)
		return r
	}
	p := providers.NewHuggingFaceProvider(apiKey)
	var text string
	r.Elapsed, err = timeIt(func() error {
		var terr error
		text, terr = p.Transcribe(ctx, wav, "ar")
		return terr
	})
	r.Pass = err == nil
	if err != nil {
		r.Detail = truncate(err.Error())
	} else {
		r.Detail = fmt.Sprintf("transcript=%q", truncate(text))
	}
	return r
}

func checkHuggingFaceTTS(ctx context.Context) checkResult {
	r := checkResult{Provider: "hf", Check: "tts-synthesize"}
	p := providers.NewHuggingFaceProvider("")
	var audio []byte
	var err error
	r.Elapsed, err = timeIt(func() error {
		var terr error
		audio, terr = p.Synthesize(ctx, "مرحبا", "ar")
		return terr
	})
	r.Pass = err == nil && len(audio) > 0
	if err != nil {
		r.Detail = truncate(err.Error())
	} else {
		r.Detail = fmt.Sprintf("%d bytes of audio", len(audio))
	}
	return r
}

func checkGoogleRESTSTT(ctx context.Context, apiKey string) checkResult {
	r := checkResult{Provider: "google", Check: "stt-transcribe"}
	if apiKey == "" {
		r.Skip = true
		r.Detail = "GCP_API_KEY not set"
		return r
	}
	wav, err := os.ReadFile("internal/speech/providers/testdata/tiny_16k_mono.wav")
	if err != nil {
		r.Detail = fmt.Sprintf("could not load fixture: %v", err)
		return r
	}
	p := providers.NewGoogleProvider(apiKey)
	var text string
	r.Elapsed, err = timeIt(func() error {
		var terr error
		text, terr = p.Transcribe(ctx, wav, "ar")
		return terr
	})
	r.Pass = err == nil
	if err != nil {
		r.Detail = truncate(err.Error())
	} else {
		r.Detail = fmt.Sprintf("transcript=%q", truncate(text))
	}
	return r
}

func checkGoogleRESTTTS(ctx context.Context, apiKey string) checkResult {
	r := checkResult{Provider: "google", Check: "tts-synthesize"}
	if apiKey == "" {
		r.Skip = true
		r.Detail = "GCP_API_KEY not set"
		return r
	}
	p := providers.NewGoogleProvider(apiKey)
	var audio []byte
	var err error
	r.Elapsed, err = timeIt(func() error {
		var terr error
		audio, terr = p.Synthesize(ctx, "مرحبا", "ar")
		return terr
	})
	r.Pass = err == nil && len(audio) > 0
	if err != nil {
		r.Detail = truncate(err.Error())
	} else {
		r.Detail = fmt.Sprintf("%d bytes of audio", len(audio))
	}
	return r
}

func checkGoogleADCSTT(ctx context.Context) checkResult {
	r := checkResult{Provider: "google-adc", Check: "stt-transcribe"}
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
		r.Skip = true
		r.Detail = "GOOGLE_APPLICATION_CREDENTIALS not set"
		return r
	}
	wav, err := os.ReadFile("internal/speech/providers/testdata/tiny_16k_mono.wav")
	if err != nil {
		r.Detail = fmt.Sprintf("could not load fixture: %v", err)
		return r
	}
	p, err := providers.NewGoogleADCProvider(ctx)
	if err != nil {
		r.Detail = truncate(err.Error())
		return r
	}
	defer func() { _ = p.Close() }()
	var text string
	r.Elapsed, err = timeIt(func() error {
		var terr error
		text, terr = p.Transcribe(ctx, wav, "ar")
		return terr
	})
	r.Pass = err == nil
	if err != nil {
		r.Detail = truncate(err.Error())
	} else {
		r.Detail = fmt.Sprintf("transcript=%q", truncate(text))
	}
	return r
}

func checkGoogleADCTTS(ctx context.Context) checkResult {
	r := checkResult{Provider: "google-adc", Check: "tts-synthesize"}
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
		r.Skip = true
		r.Detail = "GOOGLE_APPLICATION_CREDENTIALS not set"
		return r
	}
	p, err := providers.NewGoogleADCProvider(ctx)
	if err != nil {
		r.Detail = truncate(err.Error())
		return r
	}
	defer func() { _ = p.Close() }()
	var audio []byte
	r.Elapsed, err = timeIt(func() error {
		var terr error
		audio, terr = p.Synthesize(ctx, "مرحبا", "ar")
		return terr
	})
	r.Pass = err == nil && len(audio) > 0
	if err != nil {
		r.Detail = truncate(err.Error())
	} else {
		r.Detail = fmt.Sprintf("%d bytes of audio", len(audio))
	}
	return r
}

func checkGCSRoundTrip(ctx context.Context, bucket string) checkResult {
	r := checkResult{Provider: "gcs", Check: "upload-round-trip"}
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
		r.Skip = true
		r.Detail = "GOOGLE_APPLICATION_CREDENTIALS not set"
		return r
	}
	if bucket == "" {
		r.Skip = true
		r.Detail = "VOICE_STORAGE_BUCKET not set (or pass -bucket)"
		return r
	}
	uploader, err := voicenotes.NewGCSUploader(ctx, bucket)
	if err != nil {
		r.Detail = truncate(err.Error())
		return r
	}
	objectPath := fmt.Sprintf("qa/aicheck-%d.txt", time.Now().UnixNano())
	payload := []byte("sawt-go aicheck live GCS test object — safe to delete")
	var signedURL string
	r.Elapsed, err = timeIt(func() error {
		if uerr := uploader.Upload(ctx, objectPath, "text/plain", map[string]string{"source": "aicheck"}, bytes.NewReader(payload)); uerr != nil {
			return uerr
		}
		var serr error
		signedURL, serr = uploader.SignedURL(objectPath, 5*time.Minute)
		return serr
	})
	r.Pass = err == nil && signedURL != ""
	if err != nil {
		r.Detail = truncate(err.Error())
	} else {
		r.Detail = fmt.Sprintf("uploaded %s, signed URL minted", objectPath)
	}
	return r
}

// runEdgeCases runs the QA edge-case checklist against whichever providers
// have credentials present, skipping (not failing) the rest.
func runEdgeCases(ctx context.Context, cfg *config.Config, bucket string) []checkResult {
	var results []checkResult

	// HF: empty audio.
	if cfg.HfAPIKey != "" {
		r := checkResult{Provider: "hf", Check: "edge:empty-audio"}
		p := providers.NewHuggingFaceProvider(cfg.HfAPIKey)
		var err error
		r.Elapsed, err = timeIt(func() error {
			_, terr := p.Transcribe(ctx, nil, "ar")
			return terr
		})
		// An error here is the expected/correct behavior for empty audio.
		r.Pass = err != nil
		r.Detail = "expected an error for empty audio: " + truncate(errString(err))
		results = append(results, r)
	} else {
		results = append(results, checkResult{Provider: "hf", Check: "edge:empty-audio", Skip: true, Detail: "HF_API_KEY not set"})
	}

	// Google REST: empty text for TTS.
	if cfg.GcpApiKey != "" {
		r := checkResult{Provider: "google", Check: "edge:empty-tts-text"}
		p := providers.NewGoogleProvider(cfg.GcpApiKey)
		var err error
		r.Elapsed, err = timeIt(func() error {
			_, terr := p.Synthesize(ctx, "", "ar")
			return terr
		})
		r.Pass = err != nil
		r.Detail = "expected an error for empty text: " + truncate(errString(err))
		results = append(results, r)
	} else {
		results = append(results, checkResult{Provider: "google", Check: "edge:empty-tts-text", Skip: true, Detail: "GCP_API_KEY not set"})
	}

	// GCS: permission-denied / nonexistent bucket.
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		r := checkResult{Provider: "gcs", Check: "edge:nonexistent-bucket"}
		uploader, err := voicenotes.NewGCSUploader(ctx, "sawt-go-nonexistent-bucket-for-aicheck")
		if err == nil {
			r.Elapsed, err = timeIt(func() error {
				return uploader.Upload(ctx, "qa/should-fail.txt", "text/plain", nil, bytes.NewReader([]byte("x")))
			})
		}
		r.Pass = err != nil
		r.Detail = "expected an error uploading to a nonexistent bucket: " + truncate(errString(err))
		results = append(results, r)
	} else {
		results = append(results, checkResult{Provider: "gcs", Check: "edge:nonexistent-bucket", Skip: true, Detail: "GOOGLE_APPLICATION_CREDENTIALS not set"})
	}

	_ = bucket
	return results
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// truncate keeps harness output short and prevents accidentally echoing an
// oversized or sensitive response body.
func truncate(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key != "" {
			_ = os.Setenv(key, val)
		}
	}
}
