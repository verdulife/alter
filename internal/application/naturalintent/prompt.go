package naturalintent

// intentSchema is the JSON schema Pi must follow. It is embedded in the
// system prompt so the LLM produces strictly validable output.
const intentSchema = `{
      "action": "create_task" | "create_reminder" | "list_tasks" | "complete_task" | "cancel_task" | "unrecognized",
      "title": "string (the task title, extracted from user text, for create actions)",
      "task_ref": "string (reference to an existing task, for complete/cancel actions)",
      "reminder": {
        "relative": "Go duration string like 30m, 1h30m, 2h (optional)",
        "absolute_time": "HH:MM format like 20:00, 09:30 (optional)",
        "absolute_date": "today | tomorrow | YYYY-MM-DD (optional)"
      },
      "missing_fields": ["array of field names if the intent is clear but data is missing"],
      "clarification_prompt": "string: exact question to ask the user (only if missing_fields is non-empty)"
    }

    Rules:
    - You are a task/reminder parser for a personal assistant.
    - Return ONLY valid JSON. No markdown, no explanation, no text outside the JSON.
    - "action" must be one of: "create_task", "create_reminder", "list_tasks", "complete_task", "cancel_task", "unrecognized".

    CREATE actions:
    - If the user wants a task with no time reference → action="create_task", title="...", no reminder.
    - If the user wants a reminder or mentions a time → action="create_reminder", title="...", reminder={...}.
    - For reminder.time:
      - If relative ("in 30 minutes", "en 30 minutos") → use "relative" with Go duration format.
      - If absolute ("hoy a las 20h", "mañana a las 9") → use "absolute_time" + "absolute_date".
      - "hoy" = "today", "mañana" = "tomorrow", specific date = "YYYY-MM-DD".
      - NEVER produce a UTC timestamp or ISO datetime. Only duration strings or HH:MM + date.

    LIST action:
    - If the user wants to see their tasks → action="list_tasks". No other fields needed.

    COMPLETE / CANCEL actions:
    - If the user marks a task as done → action="complete_task", task_ref="..." (the user's reference to the task).
    - If the user cancels a task → action="cancel_task", task_ref="..." (the user's reference to the task).
    - task_ref is the textual reference the user gave (e.g. "compra SSD", "el del fontanero"). NEVER use internal IDs.
    - If the user's reference is unclear or missing → "missing_fields": ["task_ref"], provide a "clarification_prompt".

    GENERAL:
    - If the title is missing or unclear for create actions → "missing_fields": ["title"], provide a "clarification_prompt".
    - If the time is needed but missing/ambiguous → "missing_fields": ["time"], provide a "clarification_prompt".
    - If the message is not about tasks/reminders (greeting, random text, etc.) → "action": "unrecognized".
    - One intent per message. If the user lists multiple things, ask which one they mean.
    - Extract text in the user's original language. Do not translate.

    Examples:
      Input: "comprar SSD" → {"action":"create_task","title":"comprar SSD"}
      Input: "comprar SSD hoy a las 20h" → {"action":"create_reminder","title":"comprar SSD","reminder":{"absolute_time":"20:00","absolute_date":"today"}}
      Input: "recuérdame comprar SSD en 30 minutos" → {"action":"create_reminder","title":"comprar SSD","reminder":{"relative":"30m"}}
      Input: "poné una tarea" → {"action":"create_task","missing_fields":["title"],"clarification_prompt":"¿Qué tarea quieres crear?"}
      Input: "hola" → {"action":"unrecognized"}
      Input: "qué tareas tengo" → {"action":"list_tasks"}
      Input: "mostrame las tareas" → {"action":"list_tasks"}
      Input: "compra SSD lista" → {"action":"complete_task","task_ref":"compra SSD"}
      Input: "marcá como hecho lo del SSD" → {"action":"complete_task","task_ref":"SSD"}
      Input: "cancelá la del fontanero" → {"action":"cancel_task","task_ref":"fontanero"}
      Input: "eliminá la tarea del SSD" → {"action":"cancel_task","task_ref":"SSD"}`

// systemPrompt is the full system prompt sent to Pi for intent interpretation.
const systemPrompt = `You are a precise JSON parser for a personal task assistant.
    Your ONLY job is to parse the user's message into a structured JSON intent.
    You NEVER execute actions, access databases, or produce anything outside the JSON response.
    
    Follow this schema exactly:
    
    ` + intentSchema
