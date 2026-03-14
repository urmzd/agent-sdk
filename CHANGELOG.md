# Changelog

## 0.5.0 (2026-03-14)

### Features

- **tui**: add bubbletea-based streaming progress UI ([41ec910](https://github.com/urmzd/agent-sdk/commit/41ec9105cba903d8e762b81774bdc5fbdfec4723))

### Refactoring

- **openai**: stop leaking SDK option type in public API ([ea4bd3c](https://github.com/urmzd/agent-sdk/commit/ea4bd3c557dc57f9990e3392bb0b92e981a51760))

[Full Changelog](https://github.com/urmzd/agent-sdk/compare/v0.4.0...v0.5.0)


## Unreleased

### Features

- **tui**: add bubbletea-based streaming progress UI with spinner, agent tracking, verbose-mode helpers, and non-interactive `StreamVerbose` consumer

## 0.4.0 (2026-03-14)

### Features

- add OpenAI, Anthropic, Gemini providers, markers, structured output, file pipeline, and testing utilities ([6a694b2](https://github.com/urmzd/agent-sdk/commit/6a694b240a987224455262e713db1a3051544f81))

### Documentation

- rewrite README and AGENTS for accuracy and usability ([b03c7e6](https://github.com/urmzd/agent-sdk/commit/b03c7e6ad485f2a2e311f756b9e2af1b39771579))

[Full Changelog](https://github.com/urmzd/agent-sdk/compare/v0.3.0...v0.4.0)


## 0.3.0 (2026-03-13)

### Features

- add file upload, content negotiation, and embeddings ([e9e0410](https://github.com/urmzd/agent-sdk/commit/e9e0410e77fd4af1d1a31574ba498114c7c08933))

### Refactoring

- follow Go conventions across codebase ([ca024da](https://github.com/urmzd/agent-sdk/commit/ca024da0f03103cb546e3d941d6d8b3edbbcb0a3))
- **provider**: remove model param from ChatStream ([98d1737](https://github.com/urmzd/agent-sdk/commit/98d17374c105167bfb05715f20f75d9485844558))


## Unreleased

### Features

- **core**: add `FileContent` and `MediaType` for file upload support
- **core**: add `Embedder` interface for vector embeddings
- **core**: add `Resolver` and `Extractor` interfaces for pluggable content handling
- **core**: add `ContentNegotiator` interface for provider-aware content negotiation
- **provider**: add `ResolverRegistry`, `ExtractorRegistry`, and `PrepareMessages` for content preparation
- **provider**: add built-in `file://` and `http(s)://` URI resolvers
- **ollama**: add `OllamaEmbedder` implementing `core.Embedder`
- **ollama**: implement `ContentNegotiator` with native JPEG/PNG support
- **agent**: add `Resolvers` and `Extractors` to `AgentConfig` with automatic content preparation in agent loop

## 0.2.0 (2026-03-12)

### Features

- **ollama**: update adapter for model param and error handling ([a5afcb3](https://github.com/urmzd/agent-sdk/commit/a5afcb3533a1365de30bf114d2ecd01597fb2a38))
- **provider**: implement RetryProvider with exponential backoff ([5083f03](https://github.com/urmzd/agent-sdk/commit/5083f03ec93e13e5c5a1b8abba071bce5e8516fa))
- **provider**: implement FallbackProvider for resilience ([3939d6f](https://github.com/urmzd/agent-sdk/commit/3939d6f4a8cfd220afe492124bc9e091d5bd7c88))
- **compactor**: add data-driven CompactConfig ([55dae3b](https://github.com/urmzd/agent-sdk/commit/55dae3b7b5df8026263b8499363c98ef9e453274))
- **subagent**: add SubAgentInvoker interface ([e83dd06](https://github.com/urmzd/agent-sdk/commit/e83dd06068777a6815442667ec489a7698f152b3))
- **delta**: add execution and metadata deltas ([d520914](https://github.com/urmzd/agent-sdk/commit/d5209141e83b69972c03c0a3355c8a0085304ffd))
- **message**: remove RoleTool and restructure tool results ([6d15ee8](https://github.com/urmzd/agent-sdk/commit/6d15ee8c38fa8a5733008acde38753b1e3488266))
- **content**: add ToolResultContent and ConfigContent ([f2436ec](https://github.com/urmzd/agent-sdk/commit/f2436eccddfd163856ada5c42c8409c4f00ea60c))
- **errors**: add structured error types and classification ([faa083b](https://github.com/urmzd/agent-sdk/commit/faa083b404c436c2a09458f3f151d6114e03c4d1))
- **provider**: add model parameter and NamedProvider interface ([5940db5](https://github.com/urmzd/agent-sdk/commit/5940db50fc65f6a5bf5ddde20d7e1f642afb2d99))
- **agent**: integrate tree for persistent conversations ([bebf517](https://github.com/urmzd/agent-sdk/commit/bebf517fb3dcc1faf0ceec94be2344962697e14d))
- **tree**: add tree utilities (diff, flatten, compact) ([e84cdf1](https://github.com/urmzd/agent-sdk/commit/e84cdf1307bcd885677c01abfc0ef93e811eb27d))
- **tree**: implement branching conversation tree ([5e04fa6](https://github.com/urmzd/agent-sdk/commit/5e04fa68cef7807013793c4a413ca2dad98e8994))
- **tree**: add core tree data structures and interfaces ([c77890a](https://github.com/urmzd/agent-sdk/commit/c77890aeecc51c56646b406f4b5425dea525fd16))

### Documentation

- update AGENTS guide for new architecture ([c6b066d](https://github.com/urmzd/agent-sdk/commit/c6b066dbb1d14a1d5b7e889351681780b69b24a5))
- update README with new features and patterns ([571dee4](https://github.com/urmzd/agent-sdk/commit/571dee4a49de4d7a1beecdbaaacde7ee2650f0dd))
- add AGENTS.md and agent skill for Claude Code ([3787fad](https://github.com/urmzd/agent-sdk/commit/3787fad0df587b63ed7b67a8816615add847519d))

### Refactoring

- **agent**: integrate config resolution and sub-agent handling ([dda7207](https://github.com/urmzd/agent-sdk/commit/dda7207bc8a2dcd0497e6da9fd36da856fa9ca17))
- **tool**: add subAgentTool and Register method ([089329c](https://github.com/urmzd/agent-sdk/commit/089329ccdacecdb4517baa0aed334edc13c85828))
- **stream**: add Replay for session restoration ([f0f2901](https://github.com/urmzd/agent-sdk/commit/f0f29013eb227c719897663740e3ba11226741ca))

### Miscellaneous

- **tree**: update tree implementation for new provider interface ([8ae9c3b](https://github.com/urmzd/agent-sdk/commit/8ae9c3b2a0a289145551bfd36163f50839297e49))
- **integration**: add comprehensive integration test suite ([b489fa6](https://github.com/urmzd/agent-sdk/commit/b489fa6ab3852cd3f58468ba027d25e2b47e0311))
- **tree**: add comprehensive test suite for tree operations ([32b169f](https://github.com/urmzd/agent-sdk/commit/32b169f357564b7961860ac4308ddad7fe826858))


## 0.1.0 (2026-03-10)

### Features

- typed deltas, messages, provider interface, tool registry, event stream, ollama adapter ([ada3822](https://github.com/urmzd/agent-sdk/commit/ada382273fa9c96adb0791e8af071072430e73ba))
