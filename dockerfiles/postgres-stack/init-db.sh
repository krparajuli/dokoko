#!/bin/bash
# Initialize PostgreSQL database

# Start PostgreSQL temporarily to set up the database
service postgresql start

# Wait for PostgreSQL to be ready
until pg_isready -U postgres; do
  echo "Waiting for PostgreSQL to be ready..."
  sleep 2
done

# Create database and user if they don't exist
su - postgres -c "psql -c \"SELECT 1 FROM pg_database WHERE datname = '${POSTGRES_DB}'\" | grep -q 1 || psql -c \"CREATE DATABASE ${POSTGRES_DB}\""
su - postgres -c "psql -c \"ALTER USER postgres WITH PASSWORD '${POSTGRES_PASSWORD}'\""

# Create a sample table for demonstration
su - postgres -c "psql -d ${POSTGRES_DB} -c \"
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    email VARCHAR(100) UNIQUE NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO users (username, email) VALUES
    ('admin', 'admin@example.com'),
    ('demo', 'demo@example.com')
ON CONFLICT DO NOTHING;
\""

echo "Database initialized successfully"
