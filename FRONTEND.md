# Meeseeks Frontend

This project now includes a simple, modern web frontend built with HTMX that provides a user-friendly interface for managing environments.

## Features

🎨 **Modern UI**: Clean, responsive design with modern styling  
⚡ **HTMX Integration**: Dynamic interactions without complex JavaScript  
🔄 **Real-time Updates**: Forms and lists update dynamically  
🧪 **Mock Mode**: Test frontend without requiring ArgoCD  

## Running the Frontend

### Development Mode (with Mock Data)
```bash
make dev-mock
```
This runs the server with mock ArgoCD data, perfect for frontend development.

### Production Mode (with Real ArgoCD)
```bash
# Set your ArgoCD token
export ARGOCD_TOKEN="your-actual-token"
make run
```

## Accessing the Interface

Once the server is running, open your browser to:
- **Frontend**: http://localhost:22282
- **JSON API**: http://localhost:22282/environments

## Frontend Features

### Environment Creation Form
- **Environment Name**: Unique identifier for your environment
- **Branch**: Git branch to deploy
- **Resource Settings**: CPU, Memory, and Replica configuration
- **Environment Type**: Development, Staging, or Testing
- **Dependencies**: Comma-separated list (e.g., "postgresql,redis")
- **Environment Variables**: JSON format for custom env vars

### Environment Management
- **List View**: See all environments with status
- **Delete**: Remove environments with confirmation
- **Auto-refresh**: Environment list loads automatically

### HTMX Interactions

The frontend uses HTMX for dynamic interactions:

- **Form Submission**: Creates environments without page reload
- **Dynamic Lists**: Environment list updates in real-time
- **Inline Deletion**: Remove environments with confirmation dialogs
- **Error Handling**: Friendly error messages for validation failures

## API Compatibility

The frontend is fully compatible with the existing JSON API:

- `GET /environments` - List environments
- `POST /environments` - Create environment  
- `DELETE /environments/{id}` - Delete environment

Both HTMX and JSON requests are supported on the same endpoints.

## Development

The frontend is embedded directly in the Go application using HTML templates. No separate build process is required.

### Key Components

- **HTML Template**: Embedded in `main.go` with modern CSS
- **HTMX Integration**: Uses HTMX 1.9.10 from CDN
- **Form Handling**: Processes both JSON and form data
- **Mock Client**: Provides fake data for development

### Styling

The interface uses modern CSS with:
- CSS Grid for layouts
- Clean form styling
- Responsive design
- Success/error message styling
- Hover effects and transitions

## Testing the Frontend

1. Start the server: `make dev-mock`
2. Open http://localhost:22282
3. Try creating a test environment
4. Verify the environment appears in the list
5. Test deleting an environment

The mock mode provides sample data so you can test all functionality without ArgoCD. 