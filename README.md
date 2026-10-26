# Fleetpocket

**Run Claude Code on your GitHub repositories from your phone, and get a pull request back.**

[Website](https://andrewsl105.github.io/fleetpocket/) · [Set it up](docs/DEPLOY.md) · MIT license

<img src="docs/screenshots/demo.gif" alt="Fleetpocket demo: a task is created, Claude works on it, asks a question and opens a pull request" width="320">

Describe a change in Telegram or in the web app. Claude Code does the work in an isolated container on your own server, asks you when it needs a decision, and opens a pull request when it's done. You review it whenever you like.

## How it works

1. **Describe the change.** Type it, dictate a voice message, or attach a screenshot.
2. **Claude works on a new branch.** You can follow every step live.
3. **Answer its questions.** When Claude needs a decision, you get a message with answer buttons.
4. **Get a pull request.** You see the summary, the PR and the CI result, and can send a follow-up if something needs changing.

## Steer from wherever you are

- **Telegram bot**: start tasks and answer Claude from the chat you already have open. Give each project its own topic in a group.
- **Telegram Mini App**: the full task board inside Telegram, with projects, statuses and live progress.
- **Web app**: the same app in any browser, with a sidebar layout on desktop.

## Your keys stay with you

Fleetpocket runs on your server, for one owner: you. Claude works in a sealed container with only the code it was asked to change. It gets no GitHub token, no Docker access and no host network. Your server keeps every key and does the pushing and the pull requests itself. You sign in with a password and a code from your authenticator app.

## Screenshots

<table>
  <tr>
    <td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/tasks-phone-dark.png"><img src="docs/screenshots/tasks-phone-light.png" alt="Task list with running, waiting and paused tasks" width="200"></picture></td>
    <td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/task-running-phone-dark.png"><img src="docs/screenshots/task-running-phone-light.png" alt="Running task with its live timeline" width="200"></picture></td>
    <td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/task-question-phone-dark.png"><img src="docs/screenshots/task-question-phone-light.png" alt="Task waiting on a question, with answer buttons" width="200"></picture></td>
    <td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/task-pr-phone-dark.png"><img src="docs/screenshots/task-pr-phone-light.png" alt="Finished task with its summary, pull request and CI result" width="200"></picture></td>
  </tr>
  <tr>
    <td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/new-task-phone-dark.png"><img src="docs/screenshots/new-task-phone-light.png" alt="New task form with a prompt" width="200"></picture></td>
    <td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/projects-phone-dark.png"><img src="docs/screenshots/projects-phone-light.png" alt="Projects" width="200"></picture></td>
    <td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/settings-phone-dark.png"><img src="docs/screenshots/settings-phone-light.png" alt="Settings with usage and backend health" width="200"></picture></td>
    <td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/login-phone-dark.png"><img src="docs/screenshots/login-phone-light.png" alt="Sign in with password and TOTP code" width="200"></picture></td>
  </tr>
</table>

<picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/task-running-desktop-dark.png"><img src="docs/screenshots/task-running-desktop-light.png" alt="Desktop layout: task list and a running task side by side"></picture>

## Self-host it

You need:

- a small Linux server with Docker and a domain name;
- a Claude subscription or an Anthropic API key;
- a GitHub account, where you create your own GitHub App for Fleetpocket;
- a Telegram account, if you want the bot and the Mini App.

The [setup guide](docs/DEPLOY.md) walks you through it step by step.

## Docs

- [Setup guide](docs/DEPLOY.md): server, GitHub App, Telegram bot, updates and backups
- [Operations](docs/OPERATIONS.md): health checks, metrics, logs
- [Development](docs/DEVELOPMENT.md): running it locally and contributing

## License

[MIT](LICENSE)
