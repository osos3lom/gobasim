package workflow

import (
	"strings"
)

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

// 1. Operations Agent (Phase P1 + Phase P4 Extended)
var operationsAgent = agentSpec{
	Name: "operations",
	DefaultPrompt: "You are the operations module of Sawt, an ERP assistant for an Arabian " +
		"horse stable, talking to verified staff over WhatsApp. Use the available " +
		"tools to answer questions about horses, care plans, tasks, stalls, incidents, and veterinary appointments. " +
		"Always resolve a horse or task by id via get_horse / list_tasks before acting on it — never invent an id. " +
		"If a name search is ambiguous, ask the user to clarify instead of guessing. " +
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
		tool("list_horses",
			"List all horses, optionally filtered by breed, status, or gender.",
			map[string]PropertySchema{
				"breed":  {Type: "string", Description: "Filter by breed."},
				"status": {Type: "string", Description: "active, inactive, quarantine, off-site, sold"},
				"gender": {Type: "string", Description: "Filter by gender."},
				"limit":  {Type: "integer", Description: "Max results (default 20)."},
			}),
		tool("list_stalls",
			"List stalls, optionally filtered by barnId or status.",
			map[string]PropertySchema{
				"barnId": {Type: "string", Description: "Filter by Barn ID."},
				"status": {Type: "string", Description: "occupied, empty, maintenance, quarantine, reserved"},
				"limit":  {Type: "integer", Description: "Max results (default 20)."},
			}),
		tool("get_stall_availability",
			"Get stall availability, optionally filtered by barnId.",
			map[string]PropertySchema{
				"barnId": {Type: "string", Description: "Optional barn ID."},
			}),
		tool("assign_stall",
			"Assign a horse to a specific stall.",
			map[string]PropertySchema{
				"horseId": {Type: "string", Description: "The exact database ID of the horse."},
				"stallId": {Type: "string", Description: "The exact database ID of the stall."},
			}, "horseId", "stallId"),
		tool("register_horse",
			"Register a new horse in the system.",
			map[string]PropertySchema{
				"nameEn":  {Type: "string", Description: "English name of the horse."},
				"nameAr":  {Type: "string", Description: "Arabic name of the horse."},
				"breed":   {Type: "string", Description: "Breed of the horse."},
				"color":   {Type: "string", Description: "Color of the horse."},
				"gender":  {Type: "string", Description: "Gender of the horse."},
				"ownerId": {Type: "string", Description: "Database ID of the client owner."},
			}, "nameEn", "nameAr", "breed", "color", "gender"),
		tool("check_in_horse",
			"Check in a horse to a stall.",
			map[string]PropertySchema{
				"horseId": {Type: "string", Description: "Database ID of the horse."},
				"stallId": {Type: "string", Description: "Optional Stall ID to assign."},
			}, "horseId"),
		tool("check_out_horse",
			"Check out a horse from its stall.",
			map[string]PropertySchema{
				"horseId": {Type: "string", Description: "Database ID of the horse."},
			}, "horseId"),
		tool("report_incident",
			"Report an incident (e.g. injury or asset damage).",
			map[string]PropertySchema{
				"horseId":     {Type: "string", Description: "Database ID of the horse involved."},
				"title":       {Type: "string", Description: "Title of the incident."},
				"description": {Type: "string", Description: "Detailed description of the incident."},
				"severity":    {Type: "string", Description: "low, medium, high, critical"},
			}, "horseId", "title", "description", "severity"),
		tool("list_incidents",
			"List incidents, optionally filtered by horseId or resolved status.",
			map[string]PropertySchema{
				"horseId":  {Type: "string", Description: "Filter by horse ID."},
				"resolved": {Type: "string", Description: "true or false"},
				"limit":    {Type: "integer", Description: "Max results to return."},
			}),
		tool("book_vet_appointment",
			"Book a veterinary or farrier appointment.",
			map[string]PropertySchema{
				"horseId":     {Type: "string", Description: "Database ID of the horse."},
				"vetName":     {Type: "string", Description: "Name of the vet or farrier."},
				"type":        {Type: "string", Description: "routine, emergency, farrier, dental, vaccination"},
				"scheduledAt": {Type: "string", Description: "ISO datetime string (e.g. 2026-07-09T15:00:00Z)."},
				"notes":       {Type: "string", Description: "Optional notes."},
			}, "horseId", "vetName", "type", "scheduledAt"),
		tool("record_treatment_plan",
			"Record a veterinary treatment plan for a horse.",
			map[string]PropertySchema{
				"horseId":     {Type: "string", Description: "Database ID of the horse."},
				"diagnosis":   {Type: "string", Description: "Medical diagnosis."},
				"medications": {Type: "string", Description: "JSON array of medication objects: [{\"name\":\"Aspirin\",\"dosage\":\"1 tab\",\"frequency\":\"twice daily\",\"durationDays\":5}]."},
				"notes":       {Type: "string", Description: "Optional treatment notes."},
			}, "horseId", "diagnosis", "medications"),
	},
}

// 2. Accounting Agent (Phase P1 + Phase P2a reads + Phase P2b writes)
var accountingAgent = agentSpec{
	Name: "accounting",
	DefaultPrompt: "You are the accounting module of Sawt, an ERP assistant for an Arabian " +
		"horse stable, talking to verified staff over WhatsApp. Use the available tools to " +
		"answer questions about invoices, payments, and to record expenses or payments when asked. " +
		"Always resolve an invoice by id via list_invoices / get_invoice before acting on it — " +
		"never invent an id or an amount. Restate amounts exactly as the user said them. " +
		"If anything about an amount, invoice, or vendor is ambiguous, ask one clarifying question " +
		"instead of guessing. Once you have enough information, stop calling tools and answer in " +
		"plain text, in the same language the user used, briefly — the reply may be spoken as a " +
		"voice note. No markdown.",
	Tools: []ToolDefinition{
		tool("list_invoices",
			"List invoices, optionally filtered by status or clientId.",
			map[string]PropertySchema{
				"status":   {Type: "string", Description: "draft, sent, paid, overdue, cancelled", Enum: []string{"draft", "sent", "paid", "overdue", "cancelled"}},
				"clientId": {Type: "string", Description: "Filter invoices for a specific client."},
				"limit":    {Type: "integer", Description: "Max results to return (default 20)."},
			}),
		tool("get_invoice",
			"Get one invoice by its exact id, including line items, totals, and payment status.",
			map[string]PropertySchema{
				"invoiceId": {Type: "string", Description: "Exact invoice database ID."},
			}, "invoiceId"),
		tool("record_expense",
			"Record a business expense (e.g. a feed bill). Requires amount, category, and a unique idempotencyKey.",
			map[string]PropertySchema{
				"amount":         {Type: "number", Description: "Expense amount in SAR."},
				"category":       {Type: "string", Description: "Expense category, e.g. feed, vet, maintenance."},
				"description":    {Type: "string", Description: "Short free-text description."},
				"vendorId":       {Type: "string", Description: "Vendor database ID if known."},
				"vatAmount":      {Type: "number", Description: "Optional VAT amount."},
				"horseId":        {Type: "string", Description: "Optional horse database ID associated with this expense."},
				"expenseDate":    {Type: "string", Description: "Optional expense date (YYYY-MM-DD)."},
				"idempotencyKey": {Type: "string", Description: "Unique random string to prevent double posting."},
			}, "amount", "category", "idempotencyKey"),
		tool("record_payment",
			"Record a payment received against an invoice.",
			map[string]PropertySchema{
				"invoiceId":      {Type: "string", Description: "Exact invoice database ID the payment applies to."},
				"amount":         {Type: "number", Description: "Payment amount in SAR."},
				"method":         {Type: "string", Description: "Payment method, e.g. cash, transfer, card."},
				"idempotencyKey": {Type: "string", Description: "Unique random string to prevent double posting."},
			}, "invoiceId", "amount", "idempotencyKey"),
	},
}

// 3. Administration Agent (Phase P1 + Phase P2a reads)
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
			"List contracts, optionally filtered by clientId or status.",
			map[string]PropertySchema{
				"clientId": {Type: "string", Description: "Filter contracts for a specific client."},
				"status":   {Type: "string", Description: "active, expired, pending, draft, terminated", Enum: []string{"active", "expired", "pending", "draft", "terminated"}},
				"limit":    {Type: "integer", Description: "Max results to return (default 20)."},
			}),
		tool("get_contract",
			"Get one contract by exact id, including terms and linked client.",
			map[string]PropertySchema{
				"contractId": {Type: "string", Description: "Exact contract database ID."},
			}, "contractId"),
	},
}

// 4. Client Agent (Phase P3 Self-Service)
var clientAgent = agentSpec{
	Name: "client",
	DefaultPrompt: "You are the client self-service module of Sawt, an ERP assistant for an Arabian " +
		"horse stable, talking to a horse owner or client over WhatsApp. Use the available tools to answer " +
		"questions about their own horses, contracts, statement of account, balance, and invoices. " +
		"Do not invent information. If they ask about something they don't have access to, guide them politely. " +
		"Once you have enough information, stop calling tools and answer in plain text briefly — " +
		"the reply may be spoken as a voice note. No markdown.",
	Tools: []ToolDefinition{
		tool("list_my_horses",
			"List my horses registered at the stable.",
			map[string]PropertySchema{
				"limit": {Type: "integer", Description: "Max results to return (default 20)."},
			}),
		tool("get_my_horse",
			"Get details of one of my horses.",
			map[string]PropertySchema{
				"horseId": {Type: "string", Description: "Exact database ID of the horse."},
			}, "horseId"),
		tool("list_my_invoices",
			"List my invoices and bills.",
			map[string]PropertySchema{
				"status": {Type: "string", Description: "draft, sent, paid, overdue, cancelled", Enum: []string{"draft", "sent", "paid", "overdue", "cancelled"}},
				"limit":  {Type: "integer", Description: "Max results to return."},
			}),
		tool("get_my_balance",
			"Get my current outstanding balance.",
			map[string]PropertySchema{}),
		tool("get_my_statement",
			"Get my statement of account / transaction ledger.",
			map[string]PropertySchema{}),
		tool("list_my_contracts",
			"List my contracts and boarding agreements.",
			map[string]PropertySchema{
				"status": {Type: "string", Description: "active, expired, pending, draft, terminated", Enum: []string{"active", "expired", "pending", "draft", "terminated"}},
				"limit":  {Type: "integer", Description: "Max results to return."},
			}),
	},
}

// 5. Sales Agent (Phase P5 Sales & CRM)
var salesAgent = agentSpec{
	Name: "sales",
	DefaultPrompt: "You are the sales and CRM assistant module of Sawt, helping prospective customers " +
		"learn about stable services, available packages, booking tours, and submitting inquiries. " +
		"Always reply politely and in the language the user used, briefly. No markdown.",
	Tools: []ToolDefinition{
		tool("list_available_horses",
			"List horses currently available for sale.",
			map[string]PropertySchema{
				"breed": {Type: "string", Description: "Optional breed filter."},
				"limit": {Type: "integer", Description: "Max results to return."},
			}),
		tool("list_available_stalls",
			"List empty stalls currently available for boarding bookings.",
			map[string]PropertySchema{
				"limit": {Type: "integer", Description: "Max results to return."},
			}),
		tool("list_packages",
			"List stable service/boarding packages and pricing details.",
			map[string]PropertySchema{
				"category": {Type: "string", Description: "Optional package category filter."},
			}),
		tool("book_tour",
			"Book a stable site tour for a prospective client lead.",
			map[string]PropertySchema{
				"leadId":      {Type: "string", Description: "Optional lead database ID if known."},
				"name":        {Type: "string", Description: "Name of the prospect."},
				"phone":       {Type: "string", Description: "Phone number of the prospect."},
				"scheduledAt": {Type: "string", Description: "ISO datetime string (e.g. 2026-07-09T15:00:00Z)."},
				"notes":       {Type: "string", Description: "Optional notes."},
			}, "name", "phone", "scheduledAt"),
		tool("submit_inquiry",
			"Submit a CRM customer inquiry about boarding, breeding, purchase, etc.",
			map[string]PropertySchema{
				"name":        {Type: "string", Description: "Name of the customer."},
				"phone":       {Type: "string", Description: "Phone number of the customer."},
				"email":       {Type: "string", Description: "Optional email address."},
				"inquiryType": {Type: "string", Description: "boarding, breeding, purchase, other"},
				"notes":       {Type: "string", Description: "Optional inquiry details."},
			}, "name", "phone", "inquiryType"),
	},
}

// 6. Breeding Agent (Phase P5 Breeding Records)
var breedingAgent = agentSpec{
	Name: "breeding",
	DefaultPrompt: "You are the equine breeding assistant module of Sawt, helping breeders, barn managers, and vets " +
		"manage stallion/mare breeding bookings, pregnancy tracking, foaling, and bloodline recommendations. " +
		"Reply professionally and briefly. No markdown.",
	Tools: []ToolDefinition{
		tool("list_breeding_stock",
			"List horses registered in breeding stock (mares and stallions).",
			map[string]PropertySchema{
				"gender": {Type: "string", Description: "mare or stallion"},
				"limit":  {Type: "integer", Description: "Max results to return."},
			}),
		tool("book_breeding",
			"Book a breeding session between a mare and stallion.",
			map[string]PropertySchema{
				"mareId":      {Type: "string", Description: "Database ID of the mare."},
				"stallionId":  {Type: "string", Description: "Database ID of the stallion."},
				"bookingDate": {Type: "string", Description: "ISO date string (YYYY-MM-DD)."},
				"notes":       {Type: "string", Description: "Optional breeding notes."},
			}, "mareId", "stallionId", "bookingDate"),
		tool("get_pregnancy_status",
			"Get pregnancy/ultrasound history for a mare.",
			map[string]PropertySchema{
				"mareId": {Type: "string", Description: "Database ID of the mare."},
			}, "mareId"),
		tool("list_foals",
			"List foal records and recent births.",
			map[string]PropertySchema{
				"limit": {Type: "integer", Description: "Max results to return."},
			}),
		tool("recommend_bloodline",
			"Evaluate breeding compatibility and get bloodline recommendation for a mare and stallion.",
			map[string]PropertySchema{
				"mareId":     {Type: "string", Description: "Database ID of the mare."},
				"stallionId": {Type: "string", Description: "Database ID of the stallion."},
			}, "mareId", "stallionId"),
	},
}

// toolMinRole maps every tool to its minimum role requirement
var toolMinRole = map[string]string{
	// Operations
	"get_horse":             "viewer",
	"get_care_plan":         "viewer",
	"list_tasks":            "viewer",
	"update_task_status":    "manager",
	"list_horses":           "viewer",
	"list_stalls":           "viewer",
	"get_stall_availability": "viewer",
	"assign_stall":          "manager",
	"register_horse":        "manager",
	"check_in_horse":        "manager",
	"check_out_horse":       "manager",
	"report_incident":       "manager",
	"list_incidents":        "viewer",
	"book_vet_appointment":  "manager",
	"record_treatment_plan": "manager",

	// Accounting
	"list_invoices":         "manager",
	"get_invoice":           "manager",
	"record_expense":        "manager",
	"record_payment":        "manager",

	// Administration
	"list_clients":          "manager",
	"get_client":            "manager",
	"list_contracts":        "manager",
	"get_contract":          "manager",

	// Client
	"list_my_horses":        "client",
	"get_my_horse":          "client",
	"list_my_invoices":      "client",
	"get_my_balance":        "client",
	"get_my_statement":      "client",
	"list_my_contracts":      "client",

	// Sales
	"list_available_horses": "viewer",
	"list_available_stalls": "viewer",
	"list_packages":         "viewer",
	"book_tour":             "viewer",
	"submit_inquiry":        "viewer",

	// Breeding
	"list_breeding_stock":   "viewer",
	"book_breeding":         "manager",
	"get_pregnancy_status":  "viewer",
	"list_foals":            "viewer",
	"recommend_bloodline":   "viewer",
}

// roleHierarchy converts string roles to numerical ranks for validation
var roleHierarchy = map[string]int{
	"client":      1,
	"viewer":      2,
	"manager":     3,
	"admin":       4,
	"super_admin": 5,
}

func getMinRoleForTool(toolName string) string {
	if r, ok := toolMinRole[toolName]; ok {
		return r
	}
	return "manager" // Default safe min role for unknown tools
}

func hasRole(userRole, minRole string) bool {
	uVal := roleHierarchy[strings.ToLower(userRole)]
	mVal := roleHierarchy[strings.ToLower(minRole)]
	if mVal == 0 {
		return true // Default to allow if minRole is undefined
	}
	return uVal >= mVal
}
