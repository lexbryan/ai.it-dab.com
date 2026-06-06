# Connecting a project to the DAB AI gateway

This is the guide another DAB project (hub, accounting, …) follows to call the
LLM gateway. It is the **single source of truth** for the public credential and
endpoint contract — the examples are meant to run verbatim against a running
stack.

You talk to the gateway, never to vLLM directly. The gateway authenticates your
project, injects your key's persona, retains your conversation history
server-side, and forwards everything to the model with its own upstream secret.
Your project never sees or sends that upstream secret.

---

## 1. The two-key credential model

A credential is a **pair**:

| Half | Looks like | Stored as | Sent on every request |
| --- | --- | --- | --- |
| Public key id | `dab_pk_…` | plaintext | yes |
| Secret | `dab_sk_…` | **hashed** (never recoverable) | yes |

**Both halves are required on every request.** A request missing either half —
or with a wrong/revoked/unknown pair — gets the same generic `401`, so a caller
can never probe which half was wrong.

> The secret (`dab_sk_…`) is shown **once**, at creation, in the frontend. It is
> stored only as a hash, so it cannot be retrieved later. Copy it immediately
> and keep it somewhere safe (a secret manager / env var). If you lose it,
> revoke the key and create a new one.

Project keys are **never** forwarded to the model backend. The gateway swaps in
its own upstream secret for the model hop; your `dab_pk_`/`dab_sk_` pair stays
between your project and the gateway.

---

## 2. Getting a credential

In the admin frontend:

1. Log in and open **API keys**.
2. **Create a key**. Optionally give it a **persona** (see below) and a label.
3. **Copy the secret now.** It is revealed exactly once. The public key id
   (`dab_pk_…`) remains visible afterward; the secret (`dab_sk_…`) does not.
4. To rotate, **revoke** the key (a revoked key immediately stops
   authenticating) and create a new one. You can **edit the persona** of an
   existing key without changing its keys.

Provide the pair to your project as configuration, e.g.:

```bash
export DAB_PK="dab_pk_…"
export DAB_SK="dab_sk_…"
export DAB_GATEWAY_URL="https://gateway.internal"   # wherever the gateway runs
```

---

## 3. Persona (server-side system prompt)

Each key has an optional **persona** — a system prompt configured in the
frontend and stored with the key. The gateway injects it as the **leading
`system` message on every call**, automatically.

That means:

- Your project does **not** send a system prompt. A `system` role in your
  request is rejected (`400`).
- The persona applies on every turn, even for a fine-tuned model.
- Different keys can carry different personas; pick the key whose persona fits
  the use case.

---

## 4. The gateway endpoint

### Method & path

```
POST /v1/gateway/chat
```

### Authentication headers

Send both halves of your credential on every request:

```
X-DAB-Key-Id: dab_pk_…
X-DAB-Secret: dab_sk_…
Content-Type: application/json
```

### Request body

```jsonc
{
  "session_id": "…",        // OMIT on the first call; the gateway issues one (see §5)
  "model": "qwen2.5",       // REQUIRED on the first call; inherited by later calls
  "message": "Hello!",      // a single user turn (convenience), OR:
  "messages": [             // explicit turn(s); roles must be "user" or "assistant"
    { "role": "user", "content": "Hello!" }
  ],
  "stream": true,           // true → SSE stream (§4.2); false/omitted → one JSON reply (§4.1)

  // optional sampling parameters, passed through to the model:
  "temperature": 0.7,
  "top_p": 0.9,
  "max_tokens": 512,
  "stop": ["\n\n"]
}
```

Rules:

- Send **either** `message` (a single user turn) **or** a non-empty `messages`
  array — at least one is required.
- Do **not** include a `system` message — the persona is server-side.
- Do **not** resend prior history. The gateway already has it (it persists every
  exchange under the session id) and assembles the full context for you. Just
  send the new turn(s).
- `model` is required when starting a new conversation; on follow-ups it is
  inherited from the session and may be omitted.

### 4.1 Non-streaming response (`stream` false/omitted)

`200 OK`, `Content-Type: application/json`:

```json
{
  "session_id": "1f0e…",
  "model": "qwen2.5",
  "message": { "role": "assistant", "content": "Hi! How can I help?" },
  "usage": { "prompt_tokens": 24, "completion_tokens": 9, "total_tokens": 33 }
}
```

`session_id` is the gateway-issued conversation id — keep it for the next turn.

### 4.2 Streaming response (`stream: true`)

`200 OK`, `Content-Type: text/event-stream`. The gateway proxies the model's SSE
straight through, flushing each chunk as it arrives (no buffering). The
gateway-issued session id is surfaced two ways:

- the **`X-DAB-Session-Id`** response header, and
- a **leading `session` event** as the first frame.

The frame sequence is:

```
event: session
data: {"session_id":"1f0e…"}

data: {"choices":[{"delta":{"content":"Hi"}}]}

data: {"choices":[{"delta":{"content":"!"}}]}

data: [DONE]
```

- The `session` frame comes first and carries your session id.
- Each `data:` frame after it is an OpenAI-style chat-completion **chunk**;
  concatenate `choices[0].delta.content` across frames to rebuild the reply.
- The stream terminates with `data: [DONE]`.
- If the upstream fails **after** the stream has started, the gateway emits an
  error frame instead of `[DONE]` and persists nothing for that turn:

  ```
  event: error
  data: {"error":{"type":"upstream_error","message":"the model backend ended the stream unexpectedly"}}
  ```

A turn is only saved once `[DONE]` is reached. If the client disconnects
mid-stream, the partial turn is discarded.

---

## 5. The gateway-issued session id

You do **not** mint conversation ids — the gateway does.

1. **First request:** omit `session_id`. The gateway creates a conversation and
   returns its id (in the JSON `session_id` for non-streaming, or the
   `X-DAB-Session-Id` header / leading `session` frame for streaming).
2. **Follow-up requests:** pass that id back as `session_id`. The gateway loads
   the prior history, sends the **full** conversation (persona + history + your
   new turn) to the model, and persists the new exchange.

So your project keeps just the session id between turns — not the transcript. A
session is scoped to the key that created it; using another key's session id
returns `404`.

---

## 6. Errors

Every error (except the SSE `event: error` frame) is a JSON envelope:

```json
{ "error": { "type": "…", "message": "…" } }
```

| Status | `type` | When |
| --- | --- | --- |
| `400` | `invalid_request` | malformed JSON, unknown field, a `system` role, an empty or non-`user`/`assistant` message, no `message`/`messages`, or missing `model` on a new conversation |
| `401` | `unauthorized` | missing/blank/unknown/revoked key id or wrong secret (both halves required) |
| `404` | `not_found` | `session_id` does not belong to this key (or does not exist) |
| `429` | `rate_limited` | too many requests; a `Retry-After` header gives the back-off in seconds |
| `502` | `upstream_error` | the model backend failed to start or complete the request |
| `503` | `unavailable` | the gateway could not verify the credential or load the conversation (transient) |
| `500` | `internal_error` | the reply was produced but could not be saved |

Error messages are deliberately generic and never leak the upstream secret,
upstream URL, or model error body.

---

## 7. Copy-paste examples

Both reuse the gateway-issued session id across two turns to prove context is
retained server-side. Set `DAB_GATEWAY_URL`, `DAB_PK`, `DAB_SK` first (§2).

### curl (streaming)

```bash
# Turn 1 — no session_id; capture the gateway-issued id from the response header.
# -N disables curl's output buffering so you see chunks live.
curl -sN "$DAB_GATEWAY_URL/v1/gateway/chat" \
  -H "X-DAB-Key-Id: $DAB_PK" \
  -H "X-DAB-Secret: $DAB_SK" \
  -H "Content-Type: application/json" \
  -D /tmp/dab-headers \
  -d '{"model":"qwen2.5","message":"Hello! My name is Ada.","stream":true}'

SESSION=$(grep -i '^x-dab-session-id:' /tmp/dab-headers | tr -d '\r' | awk '{print $2}')
echo "session: $SESSION"

# Turn 2 — reuse the session id; omit model (inherited). The gateway already
# has turn 1, so just send the new message.
curl -sN "$DAB_GATEWAY_URL/v1/gateway/chat" \
  -H "X-DAB-Key-Id: $DAB_PK" \
  -H "X-DAB-Secret: $DAB_SK" \
  -H "Content-Type: application/json" \
  -d "{\"session_id\":\"$SESSION\",\"message\":\"What is my name?\",\"stream\":true}"
```

The second turn answers "Ada" — the gateway resent the full history (and the
key's persona) to the model, even though you only sent the new question.

### Node (fetch, Node 18+)

```js
const BASE = process.env.DAB_GATEWAY_URL;
const headers = {
  "X-DAB-Key-Id": process.env.DAB_PK,
  "X-DAB-Secret": process.env.DAB_SK,
  "Content-Type": "application/json",
};

// Streams one turn, printing content deltas as they arrive, and returns the
// gateway-issued session id so the next turn can continue the conversation.
async function ask(message, sessionId) {
  const res = await fetch(`${BASE}/v1/gateway/chat`, {
    method: "POST",
    headers,
    body: JSON.stringify({
      session_id: sessionId,                 // undefined on the first call
      model: sessionId ? undefined : "qwen2.5", // only needed to start a conversation
      message,
      stream: true,
    }),
  });
  if (!res.ok) {
    const { error } = await res.json();
    throw new Error(`gateway ${res.status}: ${error?.message}`);
  }
  const session = res.headers.get("X-DAB-Session-Id");

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let sep;
    while ((sep = buffer.indexOf("\n\n")) !== -1) {
      const frame = buffer.slice(0, sep);
      buffer = buffer.slice(sep + 2);
      for (const line of frame.split("\n")) {
        if (!line.startsWith("data:")) continue;   // skip `event:` lines
        const data = line.slice(5).trim();
        if (data === "[DONE]") return session;
        try {
          process.stdout.write(JSON.parse(data).choices?.[0]?.delta?.content ?? "");
        } catch {
          /* non-content frame */
        }
      }
    }
  }
  return session;
}

const session = await ask("Hello! My name is Ada.");  // first turn issues a session
console.log("\nsession:", session);
await ask("What is my name?", session);               // reuse it to keep context
```

---

## 8. Invariants worth remembering

- Present **both** keys on **every** request; either alone fails `401`.
- The secret is shown **once** and stored hashed — copy it at creation.
- The persona is server-side — never send a `system` message.
- The gateway issues and owns session ids — omit it first, then echo it back.
- You never send history and never see the upstream `VLLM_API_KEY`; the gateway
  handles both.
