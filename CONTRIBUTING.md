# Contributing

Thanks for being here. This repository exists to teach, so contributions are
judged by whether they make the protocol *clearer*, not by whether they add
features.

## What is welcome

- **Corrections.** If a comment is wrong, or a protocol detail does not match
  the spec, that is the most valuable issue you can file. Point at the spec
  section.
- **Tests that fail.** A test proving an edge case is broken is worth more than
  a patch.
- **Clarity.** Renaming a variable, splitting a long function, adding a comment
  that explains *why* rather than *what*.
- **A third language.** A Rust or Node implementation that follows the same six
  layers would be genuinely useful, and the protocol package makes it
  mechanical.

## What is not

- New transports beyond stdio and Streamable HTTP.
- Retry logic, connection pooling, or anything that turns this into a
  production SDK. The README says plainly that this is a teaching
  implementation; adding production machinery would undercut that.
- Dependencies. Both implementations build with the standard library and POSIX
  sockets alone, and that is a feature worth keeping.

## How to contribute

1. Open an issue first for anything larger than a typo, so we do not duplicate
   work.
2. Fork, branch from `main`, and keep the change to one idea per PR.
3. Run the full suite before pushing:

   ```bash
   make test          # Go tests + C++ tests
   make vet           # Go vet
   ```

4. In the PR description, explain what the change teaches. "Adds a test for
   the case where a notification arrives before the response" is a good
   description. "Improves code" is not.

## Style

- Go: `gofmt`, and comments explain reasoning rather than restating the code.
- C++: C++17, no exceptions beyond the ones already defined (`RpcError`,
  `ToolError`, `json::ParseError`), no smart pointers where a value will do.
- Commit messages in the imperative, one line for the summary.

## The one rule

If you find yourself deleting a comment because it is "obvious", stop. This
codebase is read far more often than it is run.
