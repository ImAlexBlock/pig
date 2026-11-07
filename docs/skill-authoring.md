# Authoring skills

A skill is a directory with `SKILL.md`. Pig discovers skills in this order and
the first definition of a name wins:

1. `<cwd>/.pig/skills/<name>/SKILL.md`
2. `$PIG_HOME/skills/<name>/SKILL.md`
3. every directory listed in `skills.paths`, globbed as `*/SKILL.md`

## SKILL.md

```
---
name: deploy
description: Ship the current branch to staging
command: pig-deploy
---

Long form instructions. Read them with `read skill://deploy`.
```

The front matter accepts scalars only:

| Key | Required | Meaning |
|---|---|---|
| `name` | no | defaults to the directory name |
| `description` | yes | one line advertised in the system prompt |
| `command` | no | documentation only |

A skill without a description is skipped with a warning.

Only the first 40 skills are advertised in the system prompt. The model reads a
body on demand: `read skill://deploy` or `read skill://deploy/reference.md`.
Path traversal and absolute paths are rejected.

## Tool skills

To expose tools, add `tools.json` next to `SKILL.md`:

```json
{
  "protocol": 1,
  "command": ["python3", "server.py"],
  "tools": [
    {
      "name": "weather",
      "description": "Get the weather for a city",
      "input_schema": {
        "type": "object",
        "properties": {"city": {"type": "string"}},
        "required": ["city"]
      }
    }
  ]
}
```

Tools are registered as `<skill>__<tool>`. The server starts on first use.
The protocol is newline-delimited JSON-RPC 2.0 over stdio:

```
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol":1,"skill_dir":"/abs/dir","cwd":"/abs/cwd"}}
{"jsonrpc":"2.0","id":1,"result":{"name":"weather","tools":[...]}}

{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"weather","arguments":{"city":"Paris"}}}
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"12°C"}],"isError":false}}

{"jsonrpc":"2.0","method":"shutdown"}
```

Timeouts come from `[skills]`: `call_timeout_seconds` kills a hung call,
`idle_timeout_seconds` shuts the process down after inactivity. stderr is kept
in a 8 KiB ring for error reports.

There is no hot reload: restart the daemon after editing a skill.
