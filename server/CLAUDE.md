# Anora Runner

Read docs/SPEC.md and implement Anora Runner phase by phase as described in section 10. Stop after each phase and report.

- Verify library and SDK APIs against installed versions and their docs; never invent option names.
- Record every deviation from the spec in docs/DECISIONS.md with one line of reasoning.
- Keep docs/PROTOCOL.md, docs/API.md and docs/openapi.yaml in the same commit as the code they describe.
- Security properties in SPEC sections 3 and 5 are requirements: the runner gets no GitHub token, no Docker access, no host network; tokens never touch disk.
