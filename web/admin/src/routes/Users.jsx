import React, { useEffect, useState } from 'react'
import { fetchUsers } from '../api.js'

export default function Users() {
  const [users, setUsers] = useState([])
  const [err, setErr] = useState('')

  useEffect(() => {
    fetchUsers().then(d => setUsers(d.users || [])).catch(e => setErr(e.message))
  }, [])

  if (err) return <p style={{ color: 'red' }}>{err}</p>

  return (
    <div>
      <h2>Users</h2>
      <table border="1" cellPadding="6" style={{ borderCollapse: 'collapse', width: '100%' }}>
        <thead>
          <tr><th>Email</th><th>Name</th><th>Operator</th><th>Created</th></tr>
        </thead>
        <tbody>
          {users.map(u => (
            <tr key={u.id}>
              <td>{u.email}</td>
              <td>{u.name}</td>
              <td>{u.is_operator ? 'Yes' : 'No'}</td>
              <td>{u.created_at}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
