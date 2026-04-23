import React from 'react'
import { Routes, Route, Link } from 'react-router-dom'
import Dashboard from './routes/Dashboard.jsx'
import Users from './routes/Users.jsx'
import Accounts from './routes/Accounts.jsx'
import AuditLog from './routes/AuditLog.jsx'
import Health from './routes/Health.jsx'

export default function App() {
  return (
    <div style={{ fontFamily: 'system-ui, sans-serif', maxWidth: 960, margin: '0 auto', padding: 24 }}>
      <h1>AgentHub Admin</h1>
      <nav style={{ marginBottom: 24 }}>
        <Link to="/" style={{ marginRight: 12 }}>Dashboard</Link>
        <Link to="/users" style={{ marginRight: 12 }}>Users</Link>
        <Link to="/accounts" style={{ marginRight: 12 }}>Accounts</Link>
        <Link to="/audit-log" style={{ marginRight: 12 }}>Audit Log</Link>
        <Link to="/health">Health</Link>
      </nav>
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="/users" element={<Users />} />
        <Route path="/accounts" element={<Accounts />} />
        <Route path="/audit-log" element={<AuditLog />} />
        <Route path="/health" element={<Health />} />
      </Routes>
    </div>
  )
}
