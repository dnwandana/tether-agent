# Providers

This page tells how Tether Agent connects to a language model. It explains the provider entries,
the presets, the wire formats, the API keys, the fallback chain, and the reasoning effort. The
full list of config keys is in
[docs/CONFIG.md](https://github.com/dnwandana/tether-agent/blob/main/docs/CONFIG.md).

## Provider entries

A provider entry is one model endpoint under `providers:` in `config.yaml`. Each entry has a
name that you choose. The entry `primary` is necessary. It is the primary provider, and each
session uses it when no other rule applies.

| Key | Default | Effect |
|---|---|---|
| `preset` | `""` | The id of a preset. The preset fills `format`, `base_url`, and the API key. `""` is an entry with no preset. |
| `format` | `""` | The wire format: `openai`, `anthropic`, or `responses`. An entry with no preset must set it. |
| `base_url` | `""` | The base URL of the API. |
| `api_key` | `""` | The API key. Use `${NAME}` and keep the value in `.env`. |
| `model` | `""` | The model name that the agent sends. Each entry must set it. |
| `vision` | `false` | When `true`, images go inline to this model. |
| `context_window` | `0` | The context window in tokens. `0` takes the value from the price data, else `provider.default_context_window` (128000). |
| `price_key` | `""` | The LiteLLM price key, for example `zai/glm-5.3`. Set it when the automatic price match fails. |
| `effort` | `""` | The reasoning effort of this entry. `""` sends no effort field. See [Reasoning effort](#reasoning-effort). |

The smallest config has one entry with a preset and a model:

```yaml
providers:
  primary:
    preset: openrouter
    model: z-ai/glm-5.3
```

### Load rules

The program checks the entries when it loads the config:

- An unknown `preset` stops the load. The error gives the known ids.
- The removed preset `ollama` stops the load. The error tells you to use a custom entry for a
  local server, or `preset: ollama-cloud` for Ollama Cloud.
- A `format` that is not `openai`, `anthropic`, or `responses` stops the load.
- An `effort` that is not `""` or a level of [Reasoning effort](#reasoning-effort) stops the
  load.
- An `agent.fallback` list that breaks a rule of [The fallback chain](#the-fallback-chain)
  stops the load.
- An unknown key in an entry does not stop the load. The program ignores it.

An entry that cannot serve a call is "not usable". These entries are not usable:

- An entry with no `model`.
- A preset entry with no `api_key`, when the key env of the preset has no value.
- A preset entry with no `api_key` and a `base_url` that is not the URL of the preset.

A `primary` entry that is not usable stops the load. Another entry that is not usable loads, and
the program logs `provider entry is not usable`. The fallback chain skips the entry, also when a
session override or a cron job names it. `/model` refuses the entry and shows the cause. The
provider list of `/model` marks the entry with ⚠️. `tether doctor` shows the cause for each entry.

An entry with no preset and no `api_key` is usable. The agent sends the request with no
authorization header. A local server needs this.

## Presets

A preset is a known endpoint. An entry with `preset` needs only a `model`. These rules apply:

- An empty `format` or `base_url` takes the value of the preset. A value that you set wins.
- An empty `api_key` takes the value of the key env of the preset. This occurs only when the
  `base_url` is the URL of the preset. Thus the key of a preset never goes to another host.
- The quirks of the preset apply only on the URL of the preset.
- The preset does not fill `model`.

| Wizard item | Preset | Wire format | Base URL | Key env |
|---|---|---|---|---|
| xAI | `xai` | `responses` | `https://api.x.ai/v1` | `XAI_API_KEY` |
| OpenRouter | `openrouter` | `openai` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| OpenAI | `openai` | `responses` | `https://api.openai.com/v1` | `OPENAI_API_KEY` |
| Anthropic | `anthropic` | `anthropic` | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` |
| Z.AI, Pay as you go | `zai` | `openai` | `https://api.z.ai/api/paas/v4` | `ZAI_API_KEY` |
| Z.AI, Coding Plan | `zai-coding` | `openai` | `https://api.z.ai/api/coding/paas/v4` | `ZAI_API_KEY` |
| DeepSeek | `deepseek` | `openai` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` |
| Moonshot | `moonshot` | `openai` | `https://api.moonshot.ai/v1` | `MOONSHOT_API_KEY` |
| Alibaba | `alibaba` | `openai` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_API_KEY` |
| MiniMax | `minimax` | `anthropic` | `https://api.minimax.io/anthropic` | `MINIMAX_API_KEY` |
| Ollama | `ollama-cloud` | `openai` | `https://ollama.com/v1` | `OLLAMA_API_KEY` |

The Ollama preset is Ollama Cloud. The old preset `ollama` for a local server is removed, and a
config that uses it does not load. For a local server, use a custom entry. Refer to
[Local models](#local-models).

### Quirks

Some presets change the request a little. These changes are the quirks:

| Quirk | Presets | Effect |
|---|---|---|
| `prompt_cache_key` | `xai`, `openai` | The request has `prompt_cache_key` with the session id, so the provider can reuse its prompt cache. |
| `conv_id_header` | `xai` | The request has the `x-grok-conv-id` header with the session id. |
| `openrouter_headers` | `openrouter` | The request has the `HTTP-Referer` and `X-Title` headers that name Tether Agent. |
| `reasoning_content` | `zai`, `zai-coding`, `deepseek`, `moonshot`, `alibaba` | The agent reads the `reasoning_content` of the answer and sends it back with the assistant messages. |
| `keep_thinking` | `zai` | The request asks Z.AI to keep the thinking of the earlier turns. |
| `bearer_auth` | `minimax` | The key goes in an `Authorization: Bearer` header, not in `x-api-key`. |

### Notes for some providers

**Z.AI.** Since 2026-10-16, Z.AI gives one API key for the standard API and a different key
for the Coding Plan. The key type decides the billing. The presets `zai` and `zai-coding` both
read `ZAI_API_KEY`. If you have both keys, give one entry its own variable:

```yaml
providers:
  primary:
    preset: zai-coding
    model: glm-5.3
  zai:
    preset: zai
    model: glm-5.3
    api_key: ${ZAI_PAYG_API_KEY}
```

On the Coding Plan, a request for `glm-4.7` now runs GLM-5.3-Flash. Use `glm-5.3` for the
flagship model. Source:
[Z.AI API key management](https://docs.z.ai/devpack/notice/api-key-management).

**Alibaba.** The preset URL `dashscope-intl.aliyuncs.com` still works. But the DashScope domain
gets no new features after 2026-09-30. Alibaba recommends a workspace URL:

```text
https://{WorkspaceId}.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1
```

To use it, set `base_url` and `api_key` in the entry. The preset key goes only to the preset URL,
so an entry with another `base_url` and no `api_key` is not usable. On that URL, the quirks and
the reasoning effort of the preset do not apply. The entry uses the rows of a custom `openai`
entry. Set `price_key` if the price match fails. Source:
[Model Studio regions](https://www.alibabacloud.com/help/en/model-studio/regions).

```yaml
providers:
  primary:
    preset: alibaba
    base_url: https://{WorkspaceId}.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1
    api_key: ${DASHSCOPE_API_KEY}
    model: qwen3.8-max
```

**Moonshot.** The K3 models, for example `kimi-k3.1-pro`, unlock only after a top-up of at least
$1 on your Moonshot account.

**DeepSeek.** DeepSeek discontinued `deepseek-chat` on 2026-07-24. `deepseek-flash` costs
about 4 times less than `deepseek-v4-pro`. `deepseek-v4-pro` cannot read images.

**OpenAI.** OpenAI removes `gpt-5` from the API on 2026-12-11. Use a newer model, for example
`gpt-6.1-sol`.

**MiniMax.** MiniMax retires MiniMax-M2.5 and MiniMax-M2.7 on 2026-11-18. MiniMax-M3 does not
think by default. It thinks only when the entry has an `effort` level other than `none`, or
when you send `/effort` with such a level. Then the agent
sends `thinking: {"type":"adaptive"}`. `MiniMax-M3.1-Flash` always thinks.

**Alibaba.** `qwen3.8-max` keeps its thinking between turns. The agent sends the earlier
reasoning back in `reasoning_content`, as Alibaba requires. Qwen3.8 accepts the levels `low`,
`medium`, and `xhigh`.

## Wire formats

Tether Agent has no vendor SDKs. It speaks three wire formats over plain HTTP with server-sent
events. Each request streams.

| Wire format | Request | Use it for |
|---|---|---|
| `openai` | `POST {base_url}/chat/completions` | Each endpoint that is compatible with the OpenAI Chat Completions API. Examples are OpenRouter, Z.AI, DeepSeek, Groq, Mistral, Ollama, and local servers. |
| `anthropic` | `POST {base_url}/v1/messages` | The Anthropic Messages API, and endpoints that copy it, for example MiniMax. |
| `responses` | `POST {base_url}/responses` | The OpenAI Responses API. The presets `openai` and `xai` use it. |

These details apply to each wire format:

- **`openai`** sends `Authorization: Bearer <key>` when the entry has a key. It asks for the usage
  in the last chunk of the stream.
- **`anthropic`** adds only `/messages` when `base_url` already ends in `/v1`. It sends the
  `x-api-key` header and `anthropic-version: 2023-06-01`. It marks the last 3 messages for the
  Anthropic prompt cache. When the request sets no output limit, it sends `max_tokens: 8192`
  (`provider.anthropic_max_tokens`).
- **`responses`** sends `Authorization: Bearer <key>`. It sends `store: false`, so the provider
  keeps no copy of the conversation. It asks for the encrypted reasoning, so that the next call
  can send it back.

### Reasoning replay

Some models return their reasoning with the answer. The agent saves the reasoning with the
assistant message, in the `reasoning` column of the `messages` table. The agent sends it back
only to the same issuer: the same wire format, base URL, and model. Another entry does not get
it. For this reason, one turn stays on one entry.

## API keys

Keep the API keys in `~/.tether/.env`, not in `config.yaml`. Each line of `.env` is `NAME=value`.
The file must have the mode `0600`, else the program does not start. See
[Security](Security#secrets).

An entry gets its key in one of two ways:

- **The key env of the preset.** A preset entry with no `api_key` reads the variable of the
  preset, for example `OPENROUTER_API_KEY`.
- **`api_key: ${NAME}`.** The program reads `NAME` from the process environment first, then
  from `.env`. A `${NAME}` that is not set stops the load.

```sh
# ~/.tether/.env
OPENROUTER_API_KEY=sk-or-...
DEEPSEEK_API_KEY=sk-...
```

A process that the `terminal` tool starts does not see these keys, unless
`terminal.env_passthrough` names them. The redactor masks the keys in the logs and in tool
output.

## Set up a provider with the wizard

`tether setup` asks for the primary provider in step 1, and for the fallback list in step 2.
See [Getting Started](Getting-Started). The provider menu has these items:

1. xAI
2. OpenRouter
3. OpenAI
4. Anthropic
5. Z.AI. A second question, `Plan`, gives `Pay as you go` or `Coding Plan`.
6. DeepSeek
7. Moonshot
8. Alibaba
9. MiniMax
10. Ollama. This preset is Ollama Cloud, and it needs `OLLAMA_API_KEY`.
11. Custom

For a preset, the wizard asks for the API key and the model. Before the model question, the
wizard shows the model list of the provider. Refer to [The model list](#the-model-list). For a
local model server, use Custom. Refer to [Local models](#local-models). For Custom, the wizard
asks four questions:

1. `Format (openai, anthropic, or responses)`. The default is `openai`.
2. `Base URL`. The default is `https://api.openai.com/v1`, or `https://api.anthropic.com` for
   `anthropic`.
3. `API key`. You can type `${NAME}` to use a variable that is already in `.env`.
4. `Model`.

Then the wizard sends a test call of 1 token. When the call fails, the wizard asks the questions
again. A second failure stops the setup.

The wizard writes the key to `.env` with these names:

- A preset entry uses the key env of the preset, and the config gets no `api_key`.
- When another entry already uses that variable with a different key, the wizard adds a number,
  for example `OPENROUTER_API_KEY_2`. The config then gets `api_key: ${OPENROUTER_API_KEY_2}`.
- A custom `primary` entry uses `PRIMARY_API_KEY`.
- A custom fallback entry uses `<NAME>_API_KEY`, for example `BACKUP_API_KEY` for the entry
  `backup`.

The wizard moves a key that is written in `config.yaml` to `.env`. Before it changes
`config.yaml` or `.env`, it saves a `.bak` copy.

### Step 2: the fallback list

The wizard shows the current fallback list and asks `Change the fallback list?`. When you answer
yes, the new list starts empty. Then the wizard asks `Add a fallback provider?` for each entry.

For each entry, it asks for a preset and a name. The default name is the preset id, or `custom`.
A name can contain only letters, digits, `-`, and `_`. It cannot be `primary`. An old entry that
you do not add again stays in `providers`, but it is not in the list.

## The model list

The wizard and `/model` get the model list from the provider entry. The list call uses the wire
format, the base URL, the key, and the quirks of the entry. Thus a key never goes to another
host.

| Wire format | URL | Auth header |
|---|---|---|
| `openai`, `responses` | `<base_url>/models` | `Authorization: Bearer <key>` |
| `anthropic` | `<base_url>/v1/models?limit=1000`, or `<base_url>/models?limit=1000` when the base URL ends with `/v1` | `x-api-key: <key>` and `anthropic-version`. With the `bearer_auth` quirk, `Authorization: Bearer <key>`. |

- An entry with no key sends no auth header.
- The `openrouter_headers` quirk adds the `HTTP-Referer` and `X-Title` headers, as in a chat
  call.
- The call has a timeout of 10 seconds.
- The list is newest first when the provider gives a creation time. Else the order of the
  provider stays.

### OpenRouter

An entry with the `openrouter` preset on the URL of the preset gets two groups. Both groups show
only models with tool support:

1. **Most used this week:** the first 10 models of `sort=top-weekly`.
2. **Best for agents:** the first 5 models of `sort=agentic-high-to-low` that are not in the
   first group.

The list does not show an ID that ends in `:batch`. At the model question of the wizard, a text
searches the full OpenRouter catalog. Another OpenRouter entry, for example on a proxy URL, uses
the usual list.

### Models that cannot chat

The list does not show a model ID that contains one of these texts, without regard to case:

- `embed`
- `tts`
- `speech`
- `whisper`
- `transcribe`
- `dall-e`
- `image`
- `moderation`
- `realtime`
- `audio`
- `rerank`

You can still use such a model. Type its ID. When no model stays, the list call fails with the
cause `no chat models`.

Some providers have no model list. When the list call fails, type the model ID. Refer to
[Troubleshooting](Troubleshooting#the-wizard-or-model-shows-no-model-list).

## The fallback chain

The fallback chain is a safety net for an outage. It is not a load balancer. `agent.fallback`
is an ordered list of entry names:

```yaml
providers:
  primary:
    preset: anthropic
    model: claude-sonnet-5-5
  deepseek:
    preset: deepseek
    model: deepseek-v4-pro
  openrouter:
    preset: openrouter
    model: z-ai/glm-5.3
agent:
  fallback: [deepseek, openrouter]
```

The list must not contain `primary`, a name two times, or a name that is not in `providers`.
These rules give the list:

- When `agent.fallback` is set, it is the list.
- When `agent.fallback` is absent, an entry with the name `fallback` is the only item. This is
  the old shape of the config, and it still loads.
- `agent.fallback: []` turns off the fallback chain, also when an entry `fallback` exists.

### The order of the chain

Each model call goes through the chain in this order:

1. The pinned entry, if a pin applies.
2. `primary`.
3. The entries of `agent.fallback`, in their order.

These pins put an entry first:

- the `/model` command of a session
- the model pin of a cron job
- `agent.delegation_provider`, for a child run of `delegate_task`

A name occurs only one time in the chain. A `name:model` pin changes the model only of the first
entry. Each other entry uses its own `model`.

### When the chain goes to the next entry

The chain goes to the next entry only when a call fails **before the first byte** of the answer.
The first byte is the start of a successful HTTP response. These failures count:

- A connection error.
- HTTP 5xx.
- HTTP 429, 401, 402, 403, or 408.
- An HTTP 400 whose body tells of a billing or a quota failure. The texts are `credit balance`,
  `spend limit`, `insufficient balance`, `insufficient_quota`, and `arrearage`. The match
  ignores case.

These failures do not go to the next entry:

- Another HTTP 4xx, for example a 400 for a bad request. A bad request fails on each provider,
  so the error goes to you.
- A failure after the first byte. The agent saves the partial text, and the error goes to you.
- A stream that sends no data for 300 seconds (`agent.stream_idle_timeout_s`).
- A cancel, for example `/stop`.

### Wait times and turns

- An entry that fails gets a wait time of 60 seconds (`provider.failure_skip_s`) for its model. In
  that time, the chain skips it.
- When all entries wait, the chain tries all of them.
- All sessions share the wait times. They are in memory, so a restart clears them.
- One turn stays on one entry. The next call of the turn starts at the entry that answered.
- A new turn starts at the first entry again.
- The next entry gets the full conversation, with the tool output and the memory. See
  [Security](Security) for the data that goes to each provider.

Each call that fails and goes to the next entry logs `provider fallback`. An entry that the
chain skips in its wait time logs nothing. `tether gateway` and `tether chat` write the same
line to the log. `/model` and `/new` show a warning while the first entry of the chain is in its
wait time. `tether doctor` warns when the last answer came from an entry other than the first
entry of the session.

## Reasoning effort

The reasoning effort tells a reasoning model how much it thinks before it answers. The levels,
from the lowest to the highest, are:

`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`

### Where the level comes from

The agent selects the level of each call in this order:

1. The `/effort` level of the session. It applies to each entry of the chain.
2. The `effort` of the entry that gets the call.
3. No level. The request has no effort field, so the provider uses its default.

```yaml
providers:
  primary:
    preset: openai
    model: gpt-6.1-sol
    effort: medium
```

The `/effort` command sets the level of one session:

- `/effort high` sets the level. It applies from the next message.
- `/effort default` removes the level of the session, so the `effort` of the entry applies again.
- `/effort` with no argument shows the level, the entry, the model, and the source of the level.
  The source is `this session`, `from config`, or `provider default`.

The agent saves the session level in the `effort` column of the `sessions` table, so it stays
after a restart. A `delegate_task` child and a background review copy the level of the parent
session. See [Commands](Commands).

### How each provider gets the level

Each provider accepts different levels. The agent sends the nearest level that the provider
accepts:

| Entry | Field | Levels |
|---|---|---|
| `openai` preset, custom `responses` | `reasoning.effort` | Each level as it is. |
| `openrouter` preset | `reasoning.effort` | Each level as it is. |
| `xai` preset | `reasoning.effort` | `none` and `minimal` become `low`. `max` becomes `xhigh`. |
| `anthropic` preset | `output_config.effort` | `none` and `minimal` become `low`. |
| Custom `anthropic` | `output_config.effort` | Each level as it is. |
| `zai`, `zai-coding`, `moonshot` presets | `reasoning_effort` | `none`, `minimal`, and `low` become `low`. `medium` and `high` become `high`. `xhigh` and `max` become `max`. |
| `deepseek` preset | `reasoning_effort` or `thinking` | `none` turns off the thinking. `minimal` and `low` become `low`. `medium` and `high` become `high`. `xhigh` and `max` become `max`. |
| `alibaba` preset | `enable_thinking` and `reasoning_effort` | `none` sends `enable_thinking: false`. Each other level sends `enable_thinking: true` and a level: `minimal` and `low` become `low`, `medium` stays `medium`, and `high`, `xhigh`, and `max` become `xhigh`. |
| `minimax` preset | `output_config.effort` and `thinking` | `none` sends only the effort `low`. Each other level also sends `thinking: {"type":"adaptive"}`. `minimal` becomes `low`, and each other level goes as it is. |
| `ollama-cloud` preset, custom `openai` | `reasoning_effort` | Each level as it is. |

A preset entry with a different `base_url` uses the row of its custom wire format.

### `/effort none` and the thinking

On some presets, `/effort none` cannot turn off the thinking. The agent sends the lowest level
of the provider. These presets are:

- `zai` and `zai-coding`. GLM-5.3 always thinks.
- `moonshot`. The Kimi K3 models always think.
- `minimax` with `MiniMax-M3.1-Flash`. This model returns HTTP 400 for
  `thinking: {"type":"disabled"}`.
- `xai` and `anthropic`. The effort field of these APIs has no "off" level.

The agent never sends `thinking: {"type":"disabled"}` to these presets. On the `deepseek` and
`alibaba` presets, `none` turns off the thinking. On the `minimax` preset with `MiniMax-M3`,
`none` also gives no thinking, because that model does not think without
`thinking: {"type":"adaptive"}`.

### A model that rejects the level

Some models reject the effort field. When a call with a level gets an HTTP 400 that contains
`effort`, `reasoning`, or `thinking`, the agent sends the call again with no level. When that
call works, these things occur:

- The agent logs a warning.
- The later calls to that entry and model send no level, until a restart.
- `/effort` shows that the model rejects the level, and that the provider default applies.

## Named entries for other jobs

An entry other than `primary` can serve a special job:

| Key or command | Effect |
|---|---|
| `/model <name> config` or `/model <name>:<model>` | Pins a session to the entry. The chain is the entry, then `primary`, then `agent.fallback`. `/model <name>` shows the model list of the entry. `/model clear` removes the pin. |
| `agent.delegation_provider` | The entry for `delegate_task` child runs. The child chain starts at this entry. `""` uses the `/model` pin of the parent session, with its entry and its model. A parent with no pin gives a child with no pin. |
| `agent.vision_provider` | The entry for the `view_image` tool. `""` uses `primary` when it has `vision: true`. Else the agent has no `view_image` tool. |
| `media.tts.provider`, `media.stt.provider`, `media.image.provider` | The entry for the `endpoint` backend of a media tool. `""` uses `primary`. |
| `goals.judge_provider` | The entry for the goal judge and `/goal draft`. The chain is the entry, then `primary`, then `agent.fallback`. `""` uses the chain of the session. |

```yaml
providers:
  primary:
    preset: anthropic
    model: claude-sonnet-5-5
  cheap:
    preset: openrouter
    model: z-ai/glm-5.3
    vision: true
  speech:
    preset: openai
    model: <speech model>
agent:
  delegation_provider: cheap
  vision_provider: cheap
media:
  tts:
    backend: endpoint
    provider: speech
```

These rules apply to the named entries:

- `agent.vision_provider` uses only its entry, with no fallback chain. When `view_image` uses the
  chain of `primary`, only the entries with `vision: true` get the call.
- A media endpoint uses the `base_url`, the `api_key`, and the `model` of its entry. It always
  sends the OpenAI media requests: `/audio/speech`, `/audio/transcriptions`, and
  `/images/generations`. The `format` of the entry has no effect, and it has no fallback chain.
- An entry with `vision: false` gets the messages with no images. The `[image: <path>]` text
  stays, so the model can call `view_image`. See [Tools](Tools#media).

## Prices and usage

The agent saves the cost in USD with each answer. It calculates the cost when it saves the
message, and it does not calculate it again later. The cost uses the entry and the model that
answered, so the cost of a fallback call is correct.

The agent looks for a price in this order:

1. `pricing.overrides`, by `price_key`, then by `model`.
2. The OpenRouter price data, only for a `base_url` on `openrouter.ai`.
3. The LiteLLM price data. The agent tries `price_key`, then `<prefix>/<model>`, then `model`.
   Last, it uses the one key that ends in `/<model>`, if only one exists.

The prefix comes from the host of `base_url`:

| Host | Prefix |
|---|---|
| `api.openai.com` | `openai` |
| `api.anthropic.com` | `anthropic` |
| `api.x.ai` | `xai` |
| `api.z.ai` | `zai` |
| `api.deepseek.com` | `deepseek` |
| `api.moonshot.ai` | `moonshot` |
| `dashscope-intl.aliyuncs.com` | `dashscope` |
| `api.minimax.io` | `minimax` |
| `api.groq.com` | `groq` |
| `api.mistral.ai` | `mistral` |

When no source knows the model, the log shows `the price of the model is not known`. When more
than one LiteLLM key ends in the model name, the log tells you to set `price_key`. Set
`price_key`, or set a price manually:

```yaml
providers:
  primary:
    preset: zai-coding
    model: glm-5.3
    price_key: zai/glm-5.3
pricing:
  overrides:
    my-local-model:
      input: 0      # USD for 1M tokens
      output: 0
      cache_read: 0
```

A plan entry, for example a Coding Plan, gets the API price. The key `flat_rate` does not exist
any more. `tether doctor` warns when an entry still has it.

To see the usage, run `tether usage [today|7d|30d|all]`, or send
`/usage [today|7d|30d|all|session]`. A range counts all chats. A total with an unknown price
ends with `+`. A model with no known price, or with no input and so no hit rate, shows an
empty cell. The Telegram tables show an empty cell as `–`. The terminal table (`tether usage`,
and `/usage` in `tether chat`) shows it as `—`.

## Context window and token count

The agent uses the context window to start compaction. Compaction starts when the context is
above `context.compact_threshold` of the window. The agent gets the window in this order:

1. `context_window` of the entry, when it is more than `0`.
2. The price data of the model.
3. `provider.default_context_window` (128000).

The size of the context is the prompt tokens of the last answer, as the provider gave them,
plus the cache read tokens. The agent adds an estimate for each newer message: 4 characters for
each token. The estimate also counts the saved reasoning, because the next call can send it
again.

## Local models

Tether Agent works with a local server that speaks the `openai` wire format. Examples are
Ollama, LM Studio, and llama.cpp. No preset covers a local server, so use a custom entry. The
Ollama preset, `ollama-cloud`, is for Ollama Cloud only.

```yaml
providers:
  primary:
    format: openai
    base_url: http://localhost:11434/v1
    model: qwen3.5:9b
    context_window: 64000
```

- `base_url` is the address of the server. Ollama uses port 11434. LM Studio uses
  `http://localhost:1234/v1`, and the llama.cpp server uses `http://localhost:8080/v1`.
- An entry with no preset and no `api_key` sends no auth header. Most local servers need no key.
- The provider calls do not use the private network block of the web tools, so a local address
  works.
- Set `context_window` to the context size of the server. The price data does not know most local
  models, so the agent otherwise uses 128000 (`provider.default_context_window`), and compaction
  starts too late.

Ollama uses a context of 4K tokens by default on a GPU with less than 24 GiB of VRAM. An agent
needs at least 64000 tokens. The `openai` wire format cannot send `num_ctx`, so start the server
with `OLLAMA_CONTEXT_LENGTH=64000 ollama serve`. Refer to
[Ollama context length](https://docs.ollama.com/context-length).

## Check the providers

Run `tether doctor` after each change. For each entry, it sends a test call of 1 token. The order
is `primary`, then `agent.fallback`, then the other entries by name. A preset entry has the
check name `provider <name> (<preset>)`, for example `provider primary (openrouter)`. A custom
entry has the check name `provider <name>`.

- A check fails when the entry is not usable. The detail gives the cause, for example the
  missing key env.
- A check fails when the test call fails. Then check `base_url`, `api_key`, and `model`.

See [Troubleshooting](Troubleshooting) for more fixes.
