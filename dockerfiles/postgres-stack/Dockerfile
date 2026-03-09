FROM ubuntu:22.04

# Prevent interactive prompts during installation
ENV DEBIAN_FRONTEND=noninteractive

# Install dependencies and development tools
RUN apt-get update && \
    apt-get install -y \
    curl \
    ca-certificates \
    nginx \
    postgresql \
    postgresql-contrib \
    php-fpm \
    php-pgsql \
    php-json \
    tmux \
    vim \
    supervisor \
    wget \
    git \
    build-essential \
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

# Set Go environment variables
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/root/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Install common Go tools
RUN go install golang.org/x/tools/gopls@latest && \
    go install github.com/go-delve/delve/cmd/dlv@latest && \
    go install honnef.co/go/tools/cmd/staticcheck@latest

# Install NVM and Node.js
ENV NVM_DIR="/root/.nvm"
ENV NODE_VERSION="20.11.0"
RUN curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.7/install.sh | bash && \
    . "$NVM_DIR/nvm.sh" && \
    nvm install ${NODE_VERSION} && \
    nvm alias default ${NODE_VERSION} && \
    nvm use default

# Add NVM to PATH
ENV PATH="${NVM_DIR}/versions/node/v${NODE_VERSION}/bin:${PATH}"

# Download and install Adminer
RUN mkdir -p /var/www/html/adminer && \
    wget -O /var/www/html/adminer/index.php https://github.com/vrana/adminer/releases/download/v4.8.1/adminer-4.8.1.php && \
    chown -R www-data:www-data /var/www/html/adminer

# Configure Nginx
COPY nginx.conf /etc/nginx/sites-available/default

# Configure PostgreSQL to allow local connections
RUN echo "host all all 127.0.0.1/32 md5" >> /etc/postgresql/14/main/pg_hba.conf && \
    echo "listen_addresses = 'localhost'" >> /etc/postgresql/14/main/postgresql.conf

# Create database initialization script
COPY init-db.sh /docker-entrypoint-initdb.d/
RUN chmod +x /docker-entrypoint-initdb.d/init-db.sh

# Configure Supervisor to manage all services
COPY supervisord.conf /etc/supervisor/conf.d/supervisord.conf

# Nerd Font + terminal quality (locale, JetBrains Mono Nerd Font, tmux true-color)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        locales fontconfig unzip woff2 && \
    rm -rf /var/lib/apt/lists/* && \
    locale-gen en_US.UTF-8 && \
    mkdir -p /usr/share/fonts/nerd-fonts && \
    curl -fsSL \
        "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.2.1/JetBrainsMono.zip" \
        -o /tmp/JetBrainsMono.zip && \
    unzip -j /tmp/JetBrainsMono.zip \
        "JetBrainsMonoNerdFontMono-Regular.ttf" \
        -d /usr/share/fonts/nerd-fonts/ && \
    woff2_compress \
        /usr/share/fonts/nerd-fonts/JetBrainsMonoNerdFontMono-Regular.ttf && \
    rm /usr/share/fonts/nerd-fonts/JetBrainsMonoNerdFontMono-Regular.ttf \
       /tmp/JetBrainsMono.zip && \
    fc-cache -f && \
    printf 'set -g default-terminal "tmux-256color"\nset -ga terminal-overrides ",xterm-256color:Tc"\n' \
        > /root/.tmux.conf

# Copy entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Create workspace directory
RUN mkdir -p /workspace && chmod 777 /workspace

# Set environment variables
ENV TTYD_PORT=7681
ENV TERM=xterm-256color
ENV LANG=en_US.UTF-8
ENV LC_ALL=en_US.UTF-8
ENV POSTGRES_USER=postgres
ENV POSTGRES_PASSWORD=postgres
ENV POSTGRES_DB=mydb

# Set working directory
WORKDIR /workspace

# Expose ports
EXPOSE 7681 80 443

ENTRYPOINT ["/entrypoint.sh"]
