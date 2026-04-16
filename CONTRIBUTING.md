# Contributing to cswap

Thanks for your interest in contributing to cswap! Here's how you can help.

## Getting Started

1. Fork the repository
2. Clone your fork:
   ```bash
   git clone https://github.com/<your-username>/claude-account-swap.git
   cd claude-account-swap
   ```
3. Create a feature branch:
   ```bash
   git checkout -b feature/your-feature
   ```

## Building

Requires Go 1.22+.

```bash
go build -o cswap ./cmd/cswap/
```

## Making Changes

- Keep changes focused — one feature or fix per PR
- Follow existing code style and conventions
- Test your changes manually before submitting

## Submitting a Pull Request

1. Push your branch to your fork
2. Open a PR against `main`
3. Describe what the PR does and why
4. Wait for review — the repository owner must approve before merge

## Reporting Issues

- Use GitHub Issues
- Include your OS, Go version, and steps to reproduce
- Attach debug output if relevant (`cswap --debug --list`)

## Code of Conduct

Be respectful and constructive. We're all here to build something useful.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE.md).
