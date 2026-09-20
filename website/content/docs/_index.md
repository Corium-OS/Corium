---
title: "Docs"
description: "Build an image, boot a node, and configure a cluster."
summary: "Three ways in: understand the model, run a node, or look a field up."
draft: false
weight: 999
toc: false
params:
  section:
    title: "Documentation"
    iconName: "book"
    startUrl: "/docs/guides/quickstart/"
---

Corium is an immutable, container-native Linux distribution that boots directly
into a Kubernetes node. Pick where to start.

### Run a node

[**Quick start**](/docs/guides/quickstart/) takes you from a published image to a
working Kubernetes node in four steps. If you already know where the node will
run, [**which install guide**](/docs/install/choosing/) points at the page for
your target.

### Understand the model

[**Concepts**](/docs/guides/concepts/) explains the filesystem contract, what
happens on first boot, and why the operating system is a container image. Read it
once and the rest stops being surprising.

### Look something up

[**Configuration**](/docs/reference/configuration/) specifies every field of the
`corium:` schema. [**cctl**](/docs/reference/cli/) covers every command of the
management CLI. [**Examples**](/docs/reference/examples/) has a complete,
annotated document for each common node shape.

### When it does not work

[**Troubleshooting**](/docs/guides/troubleshooting/) indexes failures by what you
observe, across every install path.
