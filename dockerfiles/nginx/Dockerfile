FROM nginx:alpine

# Install ttyd, development tools, and dependencies
RUN apk add --no-cache \
    curl \
    ca-certificates \
    tmux \
    vim \
    openssl \
    git \
    build-base \
    python3 \
    py3-pip \
    bash \
    wget

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

# Set Go environment variables
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/root/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Install common Go tools
RUN go install golang.org/x/tools/gopls@latest && \
    go install github.com/go-delve/delve/cmd/dlv@latest && \
    go install honnef.co/go/tools/cmd/staticcheck@latest

# Install NVM and Node.js (requires bash)
ENV NVM_DIR="/root/.nvm"
ENV NODE_VERSION="20.11.0"
RUN curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.7/install.sh | bash && \
    . "$NVM_DIR/nvm.sh" && \
    nvm install ${NODE_VERSION} && \
    nvm alias default ${NODE_VERSION} && \
    nvm use default

# Add NVM to PATH
ENV PATH="${NVM_DIR}/versions/node/v${NODE_VERSION}/bin:${PATH}"

# Create a simple index page
RUN echo '<!DOCTYPE html>' > /usr/share/nginx/html/index.html && \
    echo '<html><head><title>Docker Control - Nginx Container</title></head>' >> /usr/share/nginx/html/index.html && \
    echo '<body style="font-family: Arial; padding: 40px; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); color: white;">' >> /usr/share/nginx/html/index.html && \
    echo '<h1>🚀 Nginx Container Running!</h1>' >> /usr/share/nginx/html/index.html && \
    echo '<p>This container is managed by Docker Control and served through Caddy reverse proxy.</p>' >> /usr/share/nginx/html/index.html && \
    echo '<p>Hostname: <code id="hostname"></code></p>' >> /usr/share/nginx/html/index.html && \
    echo '<p>Time: <code id="time"></code></p>' >> /usr/share/nginx/html/index.html && \
    echo '<script>document.getElementById("hostname").textContent = window.location.hostname; document.getElementById("time").textContent = new Date().toLocaleString(); setInterval(() => document.getElementById("time").textContent = new Date().toLocaleString(), 1000);</script>' >> /usr/share/nginx/html/index.html && \
    echo '</body></html>' >> /usr/share/nginx/html/index.html

# Copy nginx configuration
COPY nginx.conf /etc/nginx/nginx.conf
COPY default.conf /etc/nginx/conf.d/default.conf

# Generate self-signed SSL certificate
RUN mkdir -p /etc/nginx/ssl && \
    openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
    -keyout /etc/nginx/ssl/nginx.key \
    -out /etc/nginx/ssl/nginx.crt \
    -subj "/C=US/ST=State/L=City/O=Organization/CN=localhost"

# Copy entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Create workspace directory
RUN mkdir -p /workspace && chmod 777 /workspace

# Environment variables
ENV TTYD_PORT=7681
ENV TERM=xterm-256color

# Set working directory
WORKDIR /workspace

# Expose ports
EXPOSE 80 443 7681

ENTRYPOINT ["/entrypoint.sh"]
