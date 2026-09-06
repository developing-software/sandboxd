# sandboxd — vision

sandboxd creates and manages **sandboxes**: disposable, isolated environments on
infrastructure you own, each with a live terminal and a URL to whatever it serves.

## The sandbox

A sandbox is one request turned into one running environment. You name an image, a
command and some environment; sandboxd finds a place for it, starts it, attaches a
terminal, and tears it down when the command ends, when it goes idle, or when you say so.
Nothing survives it. There is no state to migrate, no cleanup to schedule, no second
sandbox that depends on the first.

Managing sandboxes is the whole product:

- **Create** with one call, from any image that honours a small contract. Queue when the
  fleet is full; refuse when nothing in the fleet could ever run it.
- **Place** on whichever host is free and fits: a server in a rack, a laptop behind NAT,
  a Kubernetes namespace, the control plane's own machine. A caller asks for traits with
  tags and never names a machine.
- **Watch** through a browser terminal that any number of people can open, replaying
  what they missed, with the keyboard available to whoever needs it.
- **Reach** any port the sandbox opens through a link, so a web app, a notebook or an
  editor inside it is one click away.
- **End** cleanly and know why: closed, exited, idle, failed, or the host went away.

## The main use case: coding agents

The reason sandboxd exists is to run a coding agent on a repository. An application hands
over a repo, a prompt and the credentials the agent needs; the sandbox clones the repo on
its own branch, starts the agent, and a person can look over its shoulder, take the
keyboard, or walk away and come back. The agent's work leaves by `git push`. When it is
done, the sandbox is gone and the branch is what remains.

Which agent, which model, which gateway — those are choices per sandbox, made by the
image and its entry script, not by the service. A new agent is a new file in an image.

## Other uses the same machinery serves

Because the core only knows images, commands and ports, the same sandbox is useful
wherever someone needs a throwaway environment with a terminal and a URL:

- **A remote notebook.** JupyterLab on a repository, reachable through the preview link,
  on a machine with the GPU the laptop lacks.
- **An editor in the browser.** VS Code on a fresh clone, for a review or a quick fix
  from anywhere.
- **A preview environment.** Check out a branch, start the app, hand out the link, let
  it expire. A test run someone can watch, and poke at, rather than only read a log of.

None of these need anything the agent case did not already need.

## Principles

- **You own the infrastructure.** Machines dial out; nothing asks you to open a port.
  No hosted tier, no vendor sandbox service in the loop.
- **The image is the product; the core is generic.** What a sandbox does lives in its
  image. The service never learns what a repository, a prompt or a model is.
- **Secrets pass through.** A token travels with the request, lands in the process that
  needs it, and is never written down along the way.
- **Where a sandbox runs is a configuration choice.** Each kind of host is a provider,
  enabled by a block in a config file. Adding a runtime is adding a driver; adding a
  place is adding a block. Callers cannot tell them apart.
- **One terminal, raw bytes.** Supervision is a PTY, the same one a person would use. No
  parallel protocol of structured events to keep in step with every agent.
- **Small and boring.** One control plane, one embedded database, one API described in a
  written document that clients are generated from, a config file a person can read,
  static binaries.

## Who it is for

- A **parent application** that already has users and wants to offer them agent runs,
  notebooks or previews without building the machinery: one service token, one API.
- An **operator** with machines to lend: install a worker, approve it once, done.
- A **person** at the terminal, who never has to know any of the above exists.

## Out of scope

Long-lived workspaces, shared filesystems between sandboxes, persistent scrollback,
structured agent events, a chat UI, a product frontend, resource-based scheduling, a
secret store, multiple control planes, and running on somebody else's sandbox service.
Some may come later; none define the project.

## What success looks like

From a request to a live terminal in seconds, on whichever kind of host is free. A new
runtime is one driver. A new place to run is one config block. A new kind of task is one
image. And the person watching the terminal cannot tell which of them was involved.
