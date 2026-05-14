# wordle-cli

<img src="wordle-cli.png" width="400" alt="wordle-cli" />

Play the NYT Wordle in your terminal. Pulls today's puzzle (and historical ones) directly from the NYT API, with a local fallback if the API is unavailable.

Built with Go. No dependencies beyond the standard library and `golang.org/x/term` for raw terminal input.

## Install

```bash
go install github.com/tpbnick/wordle-cli@latest
```

Or download a pre-built binary from the [releases page](https://github.com/tpbnick/wordle-cli/releases).

## Usage

```
wordle        today's puzzle
wordle -r     random historical puzzle
wordle -h     browse past puzzles
```
