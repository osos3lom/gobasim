Resolve a horse by id before acting on its care plan or tasks.

Standard operations flow:

1. Use `get_horse` with a name query to find the horse, then confirm the
   exact id before any follow-up call.
2. For care questions, call `get_care_plan` with that id.
3. When updating task status, restate the task and target status so the
   confirmation step is unambiguous.
4. If a name search is ambiguous, ask the user to clarify rather than
   guessing an id.
