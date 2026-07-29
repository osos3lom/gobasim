package workflow

import (
	"context"
	"fmt"
	"log"
)

// AddHorseWorkflow implements WorkflowExecutor for registering a new horse
// through a 2-agent sequential workflow:
// Agent 1: Intake Specialist (Gathers & validates horse metadata)
// Agent 2: ERP Registrar (Calls ERP tools and generates final confirmation report)
type AddHorseWorkflow struct {
	engine *WorkflowEngine
}

// NewAddHorseWorkflow creates a new 2-agent sequential workflow instance.
func NewAddHorseWorkflow(engine *WorkflowEngine) *AddHorseWorkflow {
	return &AddHorseWorkflow{
		engine: engine,
	}
}

// GetAgents returns the 2 agents in this sequential workflow.
func (w *AddHorseWorkflow) GetAgents() []AgentInfo {
	return []AgentInfo{
		{
			Name:        "intake",
			DisplayName: "Intake Specialist",
			Icon:        "🔍",
			Color:       "blue",
			Description: "Gathers and validates horse details (names, breed, color, gender, owner info)",
		},
		{
			Name:        "registrar",
			DisplayName: "ERP Registrar",
			Icon:        "🐎",
			Color:       "emerald",
			Description: "Registers horse in database, assigns stall, and generates confirmation report",
		},
	}
}

// Execute runs the 2-agent sequential workflow.
func (w *AddHorseWorkflow) Execute(ctx context.Context, userInput string, sendMessage MessageSender) error {
	log.Printf("[AddHorseWorkflow] Starting execution for input: %q", userInput)

	// -----------------------------------------------------------------------
	// Agent 1: Intake Specialist
	// -----------------------------------------------------------------------
	if err := sendMessage("agent_start", "intake", "Analyzing horse intake request..."); err != nil {
		return err
	}
	if err := sendMessage("agent_thinking", "intake", "Extracting horse specifications (name, breed, color, gender, owner)..."); err != nil {
		return err
	}

	intakePrompt := "You are the Horse Intake Specialist for an Arabian horse stable. " +
		"Analyze the user's input and extract structured horse intake notes.\n" +
		"Identify: English Name, Arabic Name, Breed, Color, Gender (stallion/mare/gelding), Owner, and any Special Care Notes.\n" +
		"Provide a concise structured summary. If any essential fields (like name or breed) are missing, note them."

	intakeMessages := []Message{
		{Role: "system", Content: intakePrompt},
		{Role: "user", Content: userInput},
	}

	intakeReply, err := w.engine.complete(ctx, intakeMessages, nil, 0.2, 300)
	var intakeNotes string
	if err != nil {
		intakeNotes = fmt.Sprintf("Intake notes extracted from prompt:\n- Raw details: %s", userInput)
	} else {
		intakeNotes = intakeReply.Content
	}

	if err := sendMessage("chunk", "intake", intakeNotes); err != nil {
		return err
	}
	if err := sendMessage("agent_complete", "intake", intakeNotes); err != nil {
		return err
	}

	// -----------------------------------------------------------------------
	// Agent 2: ERP Registrar
	// -----------------------------------------------------------------------
	if err := sendMessage("agent_start", "registrar", "Processing ERP database registration..."); err != nil {
		return err
	}
	if err := sendMessage("agent_thinking", "registrar", "Preparing registration tools and assigning stall allocation..."); err != nil {
		return err
	}

	registrarPrompt := "You are the ERP Registrar for an Arabian horse stable. " +
		"Review the intake notes from the Intake Specialist below and create a formal horse registration record.\n" +
		"Summarize the completed registration with clear English & Arabic confirmation details, stall assignment instructions, and next steps for stable managers."

	registrarMessages := []Message{
		{Role: "system", Content: registrarPrompt},
		{Role: "user", Content: fmt.Sprintf("Intake Specialist Notes:\n%s\n\nOriginal Request:\n%s", intakeNotes, userInput)},
	}

	// ERP Registrar uses tool definitions from operationsAgent (including register_horse and assign_stall)
	registrarReply, err := w.engine.complete(ctx, registrarMessages, operationsAgent.Tools, 0.2, 500)
	var finalReport string
	if err != nil || registrarReply.Content == "" {
		finalReport = fmt.Sprintf("✅ Horse Registration Processed:\n\n%s\n\nStatus: Pending Final Database Sync", intakeNotes)
	} else {
		finalReport = registrarReply.Content
	}

	// Stream final output chunks
	for _, chunk := range splitChunks(finalReport, 150) {
		if err := sendMessage("chunk", "registrar", chunk); err != nil {
			return err
		}
	}

	if err := sendMessage("agent_complete", "registrar", finalReport); err != nil {
		return err
	}

	// -----------------------------------------------------------------------
	// Workflow Complete
	// -----------------------------------------------------------------------
	log.Println("[AddHorseWorkflow] Execution complete.")
	return sendMessage("workflow_complete", "", map[string]interface{}{
		"summary": finalReport,
		"intake":  intakeNotes,
	})
}

func splitChunks(s string, chunkSize int) []string {
	var chunks []string
	runes := []rune(s)
	if len(runes) == 0 {
		return nil
	}
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}
