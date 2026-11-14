.PHONY: install test lint run build

install:
	npm ci --no-audit --no-fund

test: install
	npm test

# Syntax-checks the server and type-checks the client by building it.
lint: install
	npm run check

build: install
	npm run build

# Two processes: the API on :8080 (signed in as LOCAL_DEV_USER) and Vite on :5173.
run: install
	npm run dev:api & npm run dev:web
