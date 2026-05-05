# pg2-agent

> AI agent wrapper with plugin architecture.
> Tries registered agents in priority order.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Run a prompt with default settings (full mode, medium model):

`pg2-agent "{{Refactor this function}}"`

- Use plan mode (read-only) for analysis tasks:

`pg2-agent --plan "{{Analyze this code}}"`

- Use a specific model size:

`pg2-agent --model {{small|medium|large}} "{{prompt}}"`

- Enable extended thinking/reasoning mode:

`pg2-agent --thinking "{{Analyze this complex algorithm}}"`

- Pipe context via stdin:

`cat {{context.txt}} | pg2-agent "{{Fill in this template}}"`

- Combine stdin context with prompt instruction:

`echo "$template" | pg2-agent "{{Fill in this PR template based on the commits}}"`

- Combine multiple flags:

`pg2-agent --plan --model large --thinking "{{Complex code review}}"`

- Show help:

`pg2-agent --help`
