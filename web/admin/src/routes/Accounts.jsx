import React, { useEffect, useState } from 'react'
import { fetchAccounts } from '../api.js'

export default function Accounts() {
  const [accounts, setAccounts] = useState([])
  const [err, setErr] = useState('')

  useEffect(() => {
    fetchAccounts().then(d => setAccounts(d.accounts || [])).catch(e => setErr(e.message))
  }, [])

  if (err) return <p style={{ color: 'red' }}>{err}</p>

  return (
    <div>
      <h2>Accounts</h2>
      <table border="1" cellPadding="6" style={{ borderCollapse: 'collapse', width: '100%' }}>
        <thead>
          <tr><th>Name</th><th>Slug</th><th>Plan</th><th>Created</th></tr>
        </thead>
        <tbody>
          {accounts.map(a => (
            <tr key={a.id}>
              <td>{a.name}</td>
              <td>{a.slug}</td>
              <td>{a.plan}</td>
              <td>{a.created_at}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
