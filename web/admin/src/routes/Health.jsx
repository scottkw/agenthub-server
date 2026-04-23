import React, { useEffect, useState } from 'react'
import { fetchHealth } from '../api.js'

export default function Health() {
  const [data, setData] = useState(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    fetchHealth().then(d => setData(d)).catch(e => setErr(e.message))
  }, [])

  if (err) return <p style={{ color: 'red' }}>{err}</p>
  if (!data) return <p>Loading...</p>

  return (
    <div>
      <h2>Health</h2>
      <pre>{JSON.stringify(data, null, 2)}</pre>
    </div>
  )
}
