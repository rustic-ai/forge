# Forge Documentation

Documentation site for Forge, the Rustic AI runtime for running guilds.

## Setup

```bash
# Install dependencies
poetry install

# Serve locally with hot reload
poetry run zensical serve

# Build static site
poetry run zensical build
```

Visit http://localhost:8000

## Structure

```
website/
├── docs/                    # Documentation source files
│   ├── index.md            # Home page
│   ├── getting-started/    # Installation, quickstart, CLI
│   ├── features/           # Capability pages
│   ├── concepts/           # Architecture, guild/agent models
│   ├── guides/             # Task-oriented guides
│   ├── use-cases/          # Scenario walkthroughs
│   ├── reference/          # CLI, API, config, glossary
│   └── assets/             # Custom CSS and images
├── mkdocs.yml              # Zensical configuration
└── pyproject.toml          # Python dependencies
```

The site uses [Zensical](https://zensical.org/) (from the creators of Material for MkDocs).

Output goes to the `site/` directory.
