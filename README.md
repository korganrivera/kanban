# Kanban Board

A real-time collaborative Kanban board with multi-user authentication, recurring tasks, dependency management, and intelligent priority calculation.

## Features

### Core Functionality
- **6-Column Workflow**: Waiting → Ready → In Progress → Blocked → Suspended → Done
- **Real-time Collaboration**: WebSocket-based live updates across all connected clients
- **Multi-user Authentication**: Secure session-based login with bcrypt password hashing
- **Task Management**: Create, edit, delete, and move tasks between columns
- **Drag-and-Drop**: Intuitive task movement between workflow states

### Advanced Features
- **Recurring Tasks**: Support for rolling and anchored recurrence patterns
  - Daily/weekly intervals
  - Specific weekday scheduling for anchored tasks
  - Lead time calculations for task readiness
- **Task Dependencies**: Define task relationships with automatic circular dependency detection
- **Priority Engine**: Automatic priority calculation based on:
  - Task importance (dependency graph analysis)
  - Urgency (deadline proximity)
  - Topological sorting for optimal task ordering
- **WIP Limits**: Configurable work-in-progress limits per column with visual indicators
- **Overdue Detection**: Visual warnings for tasks past their scheduled due date
- **Auto-claim**: Tasks automatically assigned to logged-in user when moved to "In Progress"

### User Experience
- **Dark Theme**: Modern, easy-on-the-eyes interface
- **Quick Add**: Rapid task creation from header input
- **Advanced Editor**: Full-featured sidebar for detailed task configuration
- **Task History**: Track who created and claimed each task
- **Real-time Status**: Connection status indicator with auto-reconnect

## Technology Stack

### Backend
- **Node.js** with Express
- **WebSocket** (ws) for real-time updates
- **express-session** for authentication
- **bcrypt** for password hashing
- **File-based storage** (JSON) for tasks, users, and settings

### Frontend
- **Vanilla JavaScript** (no framework dependencies)
- **WebSocket API** for real-time sync
- **Native drag-and-drop API**
- **Datetime-local input** for scheduling

## Installation

### Prerequisites
- Node.js (v14 or higher)
- npm (comes with Node.js)

### Setup

1. **Clone the repository**
   ```bash
   git clone <repository-url>
   cd kanban
   ```

2. **Install dependencies**
   ```bash
   cd server
   npm install
   ```

3. **Start the server**
   ```bash
   node index.js
   ```

4. **Access the application**
   - Open browser to: `http://localhost:3000`
   - You'll be redirected to the login page

## Usage

### First Time Setup

1. **Register an account**
   - Navigate to `http://localhost:3000`
   - Click "Register" on the login page
   - Create username (min 3 characters) and password (min 6 characters)
   - You'll be automatically logged in after registration

2. **Start using the board**
   - Create tasks using the "Quick add" input in the header
   - Or click "Advanced" for detailed task creation
   - Drag tasks between columns to update their state
   - Click on any task to edit its details

### Task Management

#### Creating Tasks
- **Quick Add**: Type title in header input and press Enter or click "Add"
- **Advanced**: Click "Advanced" button for full task creation with:
  - Title and description
  - Scheduled due date/time
  - Lead time (days before due date when task becomes ready)
  - Recurrence settings (rolling or anchored)
  - Dependencies

#### Task States
- **Waiting**: Task is scheduled but not ready yet (based on lead time)
- **Ready**: Task is ready to be claimed and worked on
- **In Progress**: Task is actively being worked on (auto-claimed by user)
- **Blocked**: Task cannot proceed (create remedy tasks to unblock)
- **Suspended**: Task is paused or has unresolved dependencies
- **Done**: Task is completed

#### Moving Tasks
- **Drag-and-drop**: Drag task cards between columns
- **Claim button**: Click "Claim" on Ready tasks to auto-move to In Progress
- **Complete button**: Click "Complete" on In Progress tasks to mark as Done

#### Recurring Tasks
1. Edit a task and set recurrence type:
   - **Rolling**: Reschedules from completion date
   - **Anchored**: Reschedules to next valid weekday
2. Set interval (days) and optionally select specific weekdays
3. Task automatically creates new instance when completed

### Dependencies

1. **Add dependency**: 
   - Edit task
   - Select dependency from dropdown in "Manage Dependencies" section
   - Click "Add Dependency"

2. **Circular dependency prevention**:
   - System automatically detects and prevents circular references
   - Error message displayed if circular dependency attempted

3. **Automatic suspension**:
   - Tasks with unresolved dependencies automatically move to "Suspended"
   - Tasks become "Ready" when all dependencies are marked "Done"

### Settings

Click "⚙️ Settings" button to configure:
- **WIP Limits**: Set maximum tasks per column
- Visual warning (red text) when limits exceeded

## Configuration

### Environment Variables

Create a `.env` file or set environment variables:

```bash
PORT=3000                          # Server port (default: 3000)
SESSION_SECRET=your-secret-here    # Session encryption key (change in production!)
```

### Data Storage

All data stored in `server/data/` directory:
- `tasks.json`: Task data
- `users.json`: User accounts (passwords hashed with bcrypt)
- `wip_limits.json`: WIP limit configuration

### WIP Limits

Default limits (configurable via Settings UI):
```javascript
{
  "Ready": null,        // No limit
  "InProgress": 5,      // Max 5 tasks
  "Blocked": 10,        // Max 10 tasks
  "Suspended": null,    // No limit
  "Done": null,         // No limit
  "Waiting": null       // No limit
}
```

## API Endpoints

### Authentication
- `POST /auth/register` - Register new user
- `POST /auth/login` - Login
- `POST /auth/logout` - Logout
- `GET /auth/whoami` - Get current user info

### Tasks
- `GET /tasks` - Get all tasks (with effective states)
- `POST /tasks` - Create new task (requires auth)
- `PATCH /tasks/:id` - Update task (requires auth)
- `DELETE /tasks/:id` - Delete task (requires auth)
- `PATCH /tasks/:id/state` - Update task state (requires auth)
- `POST /tasks/:id/remedy` - Create remedy task for blocked task (requires auth)

### Configuration
- `GET /wip-limits` - Get WIP limits
- `PATCH /wip-limits` - Update WIP limits (requires auth)

## Architecture

### Priority Calculation
The system uses a sophisticated priority engine:
1. **Dependency Graph Analysis**: Topological sort to find task importance
2. **Urgency Calculation**: Based on deadline proximity (30-day window)
3. **Weighted Score**: `Priority = 0.4 × Urgency + 0.6 × Importance`
4. **Deadlock Detection**: Identifies and marks tasks in circular dependencies

### State Management
Tasks have both **stored state** and **effective state**:
- **Stored state**: User-set state (Ready, InProgress, Blocked, etc.)
- **Effective state**: Computed state based on:
  - Scheduled due date and lead time
  - Dependency resolution
  - Recurrence pause status

### Real-time Sync
- WebSocket connection broadcasts task updates to all clients
- Automatic reconnection on connection loss
- Client-side state derivation mirrors server logic

## Development

### Project Structure
```
kanban/
├── server/
│   ├── index.js              # Main server file
│   ├── package.json          # Dependencies
│   ├── data/                 # Data storage (JSON files)
│   ├── static/               # Frontend files
│   │   ├── index.html        # Main board interface
│   │   └── login.html        # Login/register page
│   └── node_modules/         # Dependencies
└── README.md                 # This file
```

### Backup & Restore

**Create backup:**
```bash
tar -czf kanban-backup-$(date +%Y%m%d).tar.gz --exclude=node_modules kanban/
```

**Restore from backup:**
```bash
tar -xzf kanban-backup-YYYYMMDD.tar.gz
```

### Mutation Queue
All task modifications go through a mutation queue to prevent race conditions on file writes.

## Security Considerations

### Current Implementation (Local Network)
- Session-based authentication
- bcrypt password hashing (10 rounds)
- HTTP-only session cookies
- 7-day session expiration

### For Production/Remote Access
Consider adding:
- HTTPS/SSL certificates
- Stronger session secret (environment variable)
- Rate limiting on authentication endpoints
- CSRF protection
- Database instead of JSON files
- Email verification
- Password reset flow

## Known Limitations

- **Network Access**: Currently optimized for local network use
  - Some routers with AP isolation may prevent device-to-device communication
  - For remote access, configure reverse proxy or VPN
- **File-based Storage**: Not suitable for high-concurrency scenarios
- **No Email Notifications**: Task updates visible only when logged in
- **No Roles/Permissions**: All authenticated users have equal access

## Troubleshooting

### "Unable to connect" error
- Verify server is running: `node index.js`
- Check firewall: Allow port 3000
- Check router AP isolation if accessing from another device

### WebSocket shows "Disconnected"
- Check browser console for errors
- Verify server is still running
- Check if firewall is blocking WebSocket connections

### Tasks not appearing
- Check browser console for errors
- Verify you're logged in (should see username in header)
- Check `server/data/tasks.json` exists

## License

[Specify your license here]

## Contributing

[Add contribution guidelines if accepting contributions]

## Author

Created for team task management and workflow optimization.