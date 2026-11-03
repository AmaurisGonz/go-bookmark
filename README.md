Here's a README that keeps the examples minimal and instead points users to your `rest_api_commands` document for the complete list.

# Bookmark REST API

A simple RESTful API written in Go for managing bookmarks. The application stores bookmarks in memory and persists them to a JSON file so data is retained between server restarts.

## Features

- Create, read, update, and delete bookmarks (CRUD)
- Search bookmarks by keyword
- Filter bookmarks by tag
- Supports both PUT and PATCH updates
- Automatic JSON persistence
- Request validation
- Health check endpoint
- Graceful server shutdown
- Request logging

## Requirements

- Go 1.22 or newer

## Running the Server

From the project directory, start the API with:

```bash
go run .
```

The server listens on:

```
http://localhost:8080
```

## API Endpoints

| Method | Endpoint | Description |
|---------|----------|-------------|
| GET | `/healthz` | Health check |
| GET | `/bookmarks` | Retrieve all bookmarks |
| GET | `/bookmarks/{id}` | Retrieve a bookmark by ID |
| POST | `/bookmarks` | Create a bookmark |
| PUT | `/bookmarks/{id}` | Replace or update bookmark fields |
| PATCH | `/bookmarks/{id}` | Partially update bookmark fields |
| DELETE | `/bookmarks/{id}` | Delete a bookmark |

## Query Parameters

The `GET /bookmarks` endpoint supports optional query parameters.

| Parameter | Description |
|-----------|-------------|
| `tag` | Filter bookmarks by tag |
| `q` | Search bookmark title, URL, and description |

These parameters may be used individually or together.

Example:

```
GET /bookmarks?tag=go&q=tutorial
```

## Bookmark JSON Format

```json
{
  "url": "https://example.com",
  "title": "Example",
  "description": "Example bookmark",
  "tags": [
    "example",
    "reference"
  ]
}
```

## Response Codes

| Code | Meaning |
|------|---------|
| 200 | Request successful |
| 201 | Resource created |
| 204 | Resource deleted successfully |
| 400 | Invalid request |
| 404 | Bookmark not found |
| 422 | Validation error |
| 500 | Internal server error |

## Testing the API

A complete collection of example `curl` commands is included in:

```
rest_api_commands
```

This document contains examples for:

- Starting the server
- Health check
- POST requests
- GET requests
- PUT requests
- PATCH requests
- DELETE requests
- Empty command templates for each HTTP method

## Project Structure

```
.
├── main.go
├── bookmarks.json
├── rest_api_commands
└── README.md
```

## Notes

- Data is automatically saved to `bookmarks.json`.
- IDs are automatically assigned.
- Tags are normalized to lowercase and duplicate tags are removed.
- The API accepts and returns JSON.

This version is concise, avoids duplicating all of your `curl` commands, and directs users to the dedicated `rest_api_commands` file for examples. If you're submitting this for a class, you could also add a brief "Design Decisions" section describing why you chose in-memory storage with JSON persistence and graceful shutdown.
