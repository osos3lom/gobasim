package workflow

// agentSpec is a bounded agent context: a default persona prompt plus the
// only tools that agent may call. New agents register here declaratively —
// Execute()'s router does not change.
type agentSpec struct {
	Name          string
	DefaultPrompt string
	Tools         []ToolDefinition
}

func tool(name, description string, props map[string]PropertySchema, required ...string) ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDefinition{
			Name:        name,
			Description: description,
			Parameters: ParametersSchema{
				Type:       "object",
				Properties: props,
				Required:   required,
			},
		},
	}
}

var operationsAgent = agentSpec{
	Name: "operations",
	DefaultPrompt: "You are the operations module of Sawt, an ERP assistant for an Arabian " +
		"horse stable, talking to verified staff over WhatsApp. Use the available " +
		"tools to answer questions about horses, care plans, and tasks, and to " +
		"update task status when asked. Always resolve a horse or task by id via " +
		"get_horse / list_tasks before acting on it — never invent an id. If a " +
		"name search is ambiguous, ask the user to clarify instead of guessing. " +
		"Once you have enough information, stop calling tools and answer directly in plain text, " +
		"in the same language the user used, briefly — the reply may be spoken as a voice note. No markdown.",
	Tools: []ToolDefinition{
		tool("get_horse",
			"Look up a horse by exact id, or search by name (Arabic or English). Provide horseId OR nameQuery.",
			map[string]PropertySchema{
				"horseId":   {Type: "string", Description: "The exact database ID of the horse."},
				"nameQuery": {Type: "string", Description: "Name search string in English or Arabic."},
			}),
		tool("get_care_plan",
			"Get a horse's care plan (turnout minutes, feeding schedule, special instructions).",
			map[string]PropertySchema{
				"horseId": {Type: "string", Description: "The exact database ID of the horse."},
			}, "horseId"),
		tool("list_tasks",
			"List tasks, optionally filtered by status (pending/in-progress/completed/missed), assigneeId, or horseId.",
			map[string]PropertySchema{
				"status":     {Type: "string", Description: "pending, in-progress, completed, missed"},
				"assigneeId": {Type: "string", Description: "User ID of task assignee."},
				"horseId":    {Type: "string", Description: "Filter tasks for a specific horse."},
				"limit":      {Type: "integer", Description: "Max results to return (default 20)."},
			}),
		tool("update_task_status",
			"Update a task's status. status must be one of: pending, in-progress, completed, missed.",
			map[string]PropertySchema{
				"taskId": {Type: "string", Description: "Exact task database ID."},
				"status": {Type: "string", Description: "pending, in-progress, completed, missed"},
			}, "taskId", "status"),
	},
}

var accountingAgent = agentSpec{
	Name: "accounting",
	DefaultPrompt: "You are the accounting module of Sawt, an ERP assistant for an Arabian " +
		"horse stable, talking to verified staff over WhatsApp. Use the available tools to " +
		"answer questions about invoices and payments and to record expenses or payments when asked. " +
		"Always resolve an invoice by id via list_invoices / get_invoice before acting on it — " +
		"never invent an id or an amount. Restate amounts exactly as the user said them. " +
		"If anything about an amount, invoice, or vendor is ambiguous, ask one clarifying question " +
		"instead of guessing. Once you have enough information, stop calling tools and answer in " +
		"plain text, in the same language the user used, briefly — the reply may be spoken as a " +
		"voice note. No markdown.",
	Tools: []ToolDefinition{
		tool("list_invoices",
			"List invoices, optionally filtered by status (draft/sent/paid/overdue) or clientId.",
			map[string]PropertySchema{
				"status":   {Type: "string", Description: "draft, sent, paid, overdue"},
				"clientId": {Type: "string", Description: "Filter invoices for a specific client."},
				"limit":    {Type: "integer", Description: "Max results to return (default 20)."},
			}),
		tool("get_invoice",
			"Get one invoice by its exact id, including line items, totals, and payment status.",
			map[string]PropertySchema{
				"invoiceId": {Type: "string", Description: "Exact invoice database ID."},
			}, "invoiceId"),
		tool("record_expense",
			"Record a business expense (e.g. a feed bill). Requires an amount in SAR and a category.",
			map[string]PropertySchema{
				"amount":      {Type: "number", Description: "Expense amount in SAR."},
				"category":    {Type: "string", Description: "Expense category, e.g. feed, vet, maintenance."},
				"description": {Type: "string", Description: "Short free-text description."},
				"vendorId":    {Type: "string", Description: "Vendor database ID if known."},
			}, "amount", "category"),
		tool("record_payment",
			"Record a payment received against an invoice.",
			map[string]PropertySchema{
				"invoiceId": {Type: "string", Description: "Exact invoice database ID the payment applies to."},
				"amount":    {Type: "number", Description: "Payment amount in SAR."},
				"method":    {Type: "string", Description: "Payment method, e.g. cash, transfer, card."},
			}, "invoiceId", "amount"),
	},
}

var administrationAgent = agentSpec{
	Name: "administration",
	DefaultPrompt: "You are the administration module of Sawt, an ERP assistant for an Arabian " +
		"horse stable, talking to verified staff over WhatsApp. Use the available tools to answer " +
		"questions about clients and contracts. Always resolve a client or contract by id via the " +
		"list tools before referencing it — never invent an id. If a name search is ambiguous, ask " +
		"the user to clarify instead of guessing. Once you have enough information, stop calling " +
		"tools and answer in plain text, in the same language the user used, briefly — the reply " +
		"may be spoken as a voice note. No markdown.",
	Tools: []ToolDefinition{
		tool("list_clients",
			"List clients, optionally filtered by a name search (Arabic or English).",
			map[string]PropertySchema{
				"nameQuery": {Type: "string", Description: "Name search string in English or Arabic."},
				"limit":     {Type: "integer", Description: "Max results to return (default 20)."},
			}),
		tool("get_client",
			"Get one client by exact id, including contact info and linked horses.",
			map[string]PropertySchema{
				"clientId": {Type: "string", Description: "Exact client database ID."},
			}, "clientId"),
		tool("list_contracts",
			"List contracts, optionally filtered by clientId or status (active/expired/draft).",
			map[string]PropertySchema{
				"clientId": {Type: "string", Description: "Filter contracts for a specific client."},
				"status":   {Type: "string", Description: "active, expired, draft"},
				"limit":    {Type: "integer", Description: "Max results to return (default 20)."},
			}),
		tool("get_contract",
			"Get one contract by exact id, including terms and linked client.",
			map[string]PropertySchema{
				"contractId": {Type: "string", Description: "Exact contract database ID."},
			}, "contractId"),
	},
}
