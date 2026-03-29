# Contributing to Spiral Pool

**Last Updated:** 2026-03-27

Thank you for your interest in contributing to Spiral Pool! This document outlines the contribution process and requirements.

## Developer Certificate of Origin (DCO)

All contributions to Spiral Pool must be signed off under the Developer Certificate of Origin (DCO). This is a legally binding statement that you have the right to submit the contribution under the project's license (BSD-3-Clause).

### What is the DCO?

The DCO is a per-commit sign-off that certifies you wrote (or have the right to submit) the code you're contributing. It protects both you and the project by creating a clear legal record of contributions.

Read the full DCO text in the [DCO](DCO) file.

### How to Sign Off

Add a `Signed-off-by` line to your commit messages:

```
Signed-off-by: Your Name <your.email@example.com>
```

You can do this automatically by using the `-s` or `--signoff` flag with `git commit`:

```bash
git commit -s -m "Your commit message"
```

### Why We Require DCO

1. **Legal Clarity**: Establishes clear provenance of all code
2. **License Protection**: Ensures all contributions are BSD-3-Clause compatible
3. **Contributor Protection**: Provides legal protection for contributors
4. **Project Integrity**: Maintains clean intellectual property chain

### Irrevocability of License Grant

**IMPORTANT:** In addition to the DCO sign-off, by submitting a contribution you grant an **irrevocable**, worldwide, royalty-free license to use, copy, modify, and distribute your contribution under the BSD-3-Clause license. This irrevocability is a project-specific requirement that supplements the DCO.

This license grant:
- **Cannot be revoked** once the contribution is merged
- **Survives** any future disagreement between you and the project
- **Applies** to the contribution as submitted, not to your other work
- **Does not transfer copyright** — you retain ownership of your contribution
- **Does not grant patent rights** — see Patent Notice in License section below

This irrevocability is standard for open source contributions and is necessary for the project and its users to rely on contributed code. If you do not agree to an irrevocable license grant, do not submit contributions.

## Contribution Guidelines

### Before Contributing

1. **Check existing issues** to see if your change is already being discussed
2. **Open an issue first** for significant changes to discuss the approach
3. **Read the codebase** to understand existing patterns and conventions

### Code Requirements

- Follow existing code style and formatting
- Include appropriate SPDX license headers in new files:
  ```go
  // SPDX-License-Identifier: BSD-3-Clause
  // SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
  ```
- Write tests for new functionality
- Update documentation for user-facing changes
- Do not introduce dependencies with incompatible licenses (GPL, AGPL, proprietary)

### Commit Messages

- Use clear, descriptive commit messages
- Start with a short summary (50 chars or less)
- Include detailed description if needed
- Always include DCO sign-off

Example:
```
Add Scrypt support for Litecoin mining

Implements Scrypt algorithm with configurable N/R/P parameters.
Adds difficulty calculation for Scrypt-based coins.

Signed-off-by: Your Name <your.email@example.com>
```

### Pull Request Process

1. Fork the repository
2. Create a feature branch from `main`
3. Make your changes with DCO sign-off on all commits
4. Run tests: `cd src/stratum && go test ./...`
5. Submit pull request with clear description
6. Address review feedback

### AI-Generated Code Policy

Contributors may use AI coding assistants (e.g., GitHub Copilot, Claude, ChatGPT) as development tools. However:

- **Contributors remain responsible** for all code they submit, regardless of how it was produced
- AI-generated code must meet the same quality, security, and licensing standards as manually written code
- Contributors must **review and understand** all AI-generated code before submitting
- The DCO sign-off certifies that the contributor has the right to submit the code — this applies equally to AI-assisted contributions
- If an AI tool reproduces copyrighted code from its training data, the contributor bears responsibility for any resulting infringement

All Spiral Pool source code was written, reviewed, and approved by human contributors. AI tools were used as development aids — similar to IDEs, linters, and static analysis tools — for code review, debugging, and architectural analysis. AI tools were not used as autonomous code generators. All contributions are human-authored works accepted by project maintainers under the DCO process.

### Unacceptable Contributions

The following will not be accepted:

- Code without DCO sign-off
- Code copied from projects with incompatible licenses
- Code that introduces security vulnerabilities
- Features that facilitate illegal activity
- Contributions that violate third-party intellectual property rights

## License

By contributing to Spiral Pool, you agree that your contributions will be licensed under the BSD-3-Clause license. See [LICENSE](LICENSE) for the full license text.

**Patent Notice:** The BSD-3-Clause license grants copyright permissions only. No patent license is granted by the project or by any contributor. Contributors do not grant any patent rights through the DCO sign-off or through contributing to this project. See the PATENT DISCLAIMER section in [LICENSE](LICENSE) for details.

## Legal Notice

"Spiral Pool Contributors" is a collective designation for individuals who have contributed to this project. It is not a legal entity, corporation, partnership, or organization. Each contributor retains their own legal status and is individually responsible for their contributions.

Contributions are made on an individual basis under the DCO. No contributor speaks for or represents other contributors unless explicitly authorized.

**Donations and Compensation:** Contributions to this project are made on a voluntary, uncompensated basis. No contributor is entitled to any share of donations, revenue, or other financial consideration by virtue of contributing. Donations listed in the project README are received by individual maintainers in their personal capacity and are not project funds subject to distribution. Contributing does not create any employment, partnership, joint venture, or revenue-sharing arrangement.

## Questions?

If you have questions about contributing, please open an issue for discussion or reach out via [@SpiralMiner](https://x.com/SpiralMiner) on X.

---

*This contributing guide is part of the Spiral Pool v2.0.1 release.*
*Made with 💙 from Canada 🍁 — ☮️✌️Peace and Love to the World 🌎 ❤️*
