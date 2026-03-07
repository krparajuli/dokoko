#!/bin/bash
set -e

# Initialize PostgreSQL data directory if it doesn't exist
if [ ! -f /var/lib/postgresql/14/main/PG_VERSION ]; then
    echo "Initializing PostgreSQL data directory..."
    mkdir -p /var/lib/postgresql/14/main
    chown -R postgres:postgres /var/lib/postgresql
    su - postgres -c "/usr/lib/postgresql/14/bin/initdb -D /var/lib/postgresql/14/main"

    # Run database initialization script
    /docker-entrypoint-initdb.d/init-db.sh

    # Stop PostgreSQL (supervisor will restart it)
    service postgresql stop
fi

# Create a welcome page
cat > /var/www/html/index.html <<EOF
<!DOCTYPE html>
<html>
<head>
    <title>PostgreSQL Stack</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            max-width: 800px;
            margin: 50px auto;
            padding: 20px;
            background: #f5f5f5;
        }
        .container {
            background: white;
            padding: 30px;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        h1 { color: #333; }
        .service {
            background: #e3f2fd;
            padding: 15px;
            margin: 10px 0;
            border-radius: 4px;
            border-left: 4px solid #2196f3;
        }
        .service h3 { margin-top: 0; color: #1976d2; }
        code {
            background: #f5f5f5;
            padding: 2px 6px;
            border-radius: 3px;
            font-family: 'Courier New', monospace;
        }
        a {
            color: #2196f3;
            text-decoration: none;
        }
        a:hover {
            text-decoration: underline;
        }
    </style>
    <script>
        // Preserve token in links
        window.addEventListener('DOMContentLoaded', function() {
            const urlParams = new URLSearchParams(window.location.search);
            const token = urlParams.get('token');
            if (token) {
                document.querySelectorAll('a[data-preserve-token]').forEach(function(link) {
                    const url = new URL(link.href, window.location.href);
                    url.searchParams.set('token', token);
                    link.href = url.toString();
                });
            }
        });
    </script>
</head>
<body>
    <div class="container">
        <h1>🐘 PostgreSQL Stack Container</h1>
        <p>This container includes PostgreSQL database with Adminer for database management.</p>

        <div class="service">
            <h3>🗄️ PostgreSQL Database</h3>
            <p><strong>Host:</strong> localhost (internal)</p>
            <p><strong>Port:</strong> 5432</p>
            <p><strong>User:</strong> <code>postgres</code></p>
            <p><strong>Password:</strong> <code>postgres</code></p>
            <p><strong>Database:</strong> <code>mydb</code></p>
        </div>

        <div class="service">
            <h3>📊 Adminer (Database GUI)</h3>
            <p><strong>Access:</strong> <a href="adminer/" data-preserve-token>adminer/</a></p>
            <p>Click above to manage your PostgreSQL database with a visual interface.</p>
            <p><strong>Login with:</strong></p>
            <ul>
                <li>System: <code>PostgreSQL</code></li>
                <li>Server: <code>localhost</code></li>
                <li>Username: <code>postgres</code></li>
                <li>Password: <code>postgres</code></li>
                <li>Database: <code>mydb</code></li>
            </ul>
        </div>

        <div class="service">
            <h3>💻 Terminal Access</h3>
            <p>Access via the Terminal button in the Docker Control UI</p>
            <p>Connect to PostgreSQL: <code>psql -U postgres -d mydb</code></p>
        </div>

        <h2>Sample Data</h2>
        <p>A <code>users</code> table has been created with sample data:</p>
        <pre><code>SELECT * FROM users;</code></pre>
    </div>
</body>
</html>
EOF

# Create log directory for supervisor
mkdir -p /var/log/supervisor

# Start all services with Supervisor
echo "Starting services..."
exec /usr/bin/supervisord -c /etc/supervisor/conf.d/supervisord.conf
