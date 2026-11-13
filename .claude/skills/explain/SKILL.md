---
name: explain
description: Explain a pull request, issue, plan, alert or concept in plain language for a maintainer or operator who knows general computing and security but not every component here. Use when the user says "explain", "what does this mean", "I don't understand", "ELI5", "walk me through", or asks what a PR or change actually does.
---

# Explain it plainly

The reader wants to understand what they are approving. Assume they know general
computing and security ideas (processes, networks, TLS, permissions), but not
the specific components or history of this project. Your job is understanding, not
completeness.

## Gather first

- A PR or issue: read its body, its diff or checklist, linked issues, and any
  operator steps. For a PR, use the forge CLI (for example `gh pr view <n>` or `fj pr view <n>`) and `git diff origin/main...<branch> --stat`.
- A concept or alert: find where it lives in this repo and how it is used here.
- Do not guess. If something is unknown, say so.

## Then answer in this order

1. **In one sentence:** what this is.
2. **Why it exists:** the problem it solves today, with a concrete example from
   this project (a real component, error or scenario).
3. **How it works:** a simple model, with one analogy if it helps, then the
   real mechanism in a few steps. Name each moving part once, with a short
   definition.
4. **What changes for the operator:** what they would see or do differently, and what
   stays the same.
5. **Risks and undo:** what could break, how likely it is, and the exact way
   back.
6. **What the reader needs to decide or do:** each decision with the recommended
   answer and why, and any operator step as a copy-paste command with a
   one-line explanation.
7. **Glossary:** every term used above that the reader might not know, one line each.

Keep it short: plain sentences, no unexplained acronyms, and no walls of file
paths. Offer to go deeper on any part instead of front-loading everything.
If a picture would help (a flow between services, before and after), draw a
small diagram, and use a visual or diagram tool if one is available.
