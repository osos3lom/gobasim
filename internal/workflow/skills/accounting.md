Reconcile stable invoices and payments before quoting a client balance.

When a staff member asks about money owed:

1. Call `list_invoices` for the client to see outstanding invoices.
2. Never state a balance from memory — always derive it from the latest
   invoice and payment records returned by the tools.
3. Financial writes (`record_payment`, `record_expense`) are high-risk and
   always require an explicit "yes" confirmation before they run. State the
   exact amount and target invoice in the confirmation question.
4. Report figures in Saudi Riyal (SAR) and keep replies short — they may be
   spoken aloud as a voice note.
