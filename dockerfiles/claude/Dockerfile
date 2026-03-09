FROM ubuntu:22.04

# Prevent interactive prompts during installation
ENV DEBIAN_FRONTEND=noninteractive

# Install dependencies, development tools, and utilities
RUN apt-get update && \
    apt-get install -y \
    curl \
    ca-certificates \
    tmux \
    vim \
    git \
    build-essential \
    sudo \
    wget \
    python3 \
    python3-pip \
    python3-venv \
    && rm -rf /var/lib/apt/lists/*

# Install ttyd 1.7.7 from GitHub releases (arch-aware)
RUN set -eux; \
    ARCH=$(uname -m); \
    case "$ARCH" in \
      x86_64)  TTYD_ARCH="x86_64" ;; \
      aarch64) TTYD_ARCH="aarch64" ;; \
      armv7l)  TTYD_ARCH="arm" ;; \
      *) echo "Unsupported arch: $ARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/tsl0922/ttyd/releases/download/1.7.7/ttyd.${TTYD_ARCH}" \
         -o /usr/local/bin/ttyd && \
    chmod +x /usr/local/bin/ttyd

# Install Go 1.22.0 (arch-aware)
RUN set -eux; \
    ARCH=$(uname -m); \
    case "$ARCH" in \
      x86_64)  GO_ARCH="amd64" ;; \
      aarch64) GO_ARCH="arm64" ;; \
      armv7l)  GO_ARCH="armv6l" ;; \
      *) echo "Unsupported arch: $ARCH" >&2; exit 1 ;; \
    esac; \
    wget -q "https://go.dev/dl/go1.22.0.linux-${GO_ARCH}.tar.gz" -O /tmp/go.tar.gz && \
    tar -C /usr/local -xzf /tmp/go.tar.gz && \
    rm /tmp/go.tar.gz

# Set Go environment variables (system-wide)
ENV PATH="/usr/local/go/bin:${PATH}"

# Create a user for running Claude Code (it doesn't run well as root)
RUN useradd -m -s /bin/bash claude && \
    echo "claude ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers

# Switch to claude user for installation
USER claude
WORKDIR /home/claude

# Install Claude Code
RUN curl -fsSL https://claude.ai/install.sh | bash

# Add Claude to PATH for the user
ENV PATH="/home/claude/.local/bin:${PATH}"

# Set Go environment for claude user
ENV GOPATH="/home/claude/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Install common Go tools as claude user
RUN go install golang.org/x/tools/gopls@latest && \
    go install github.com/go-delve/delve/cmd/dlv@latest && \
    go install honnef.co/go/tools/cmd/staticcheck@latest

# Install NVM and Node.js as claude user
ENV NVM_DIR="/home/claude/.nvm"
ENV NODE_VERSION="20.11.0"
RUN curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.7/install.sh | bash && \
    . "$NVM_DIR/nvm.sh" && \
    nvm install ${NODE_VERSION} && \
    nvm alias default ${NODE_VERSION} && \
    nvm use default

# Add NVM to PATH
ENV PATH="${NVM_DIR}/versions/node/v${NODE_VERSION}/bin:${PATH}"

# Switch back to root for entrypoint setup
USER root

# Copy entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Create workspace directory and set ownership
RUN mkdir -p /workspace && chown claude:claude /workspace && chmod 755 /workspace

# Set environment variables
ENV TTYD_PORT=7681
ENV TERM=xterm-256color

# Expose ttyd port
EXPOSE 7681

# Switch to claude user for runtime
USER claude
WORKDIR /workspace

ENTRYPOINT ["/entrypoint.sh"]
