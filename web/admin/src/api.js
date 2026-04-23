const BASE = '/api/admin'

async function api(path) {
  const res = await fetch(BASE + path, { credentials: 'same-origin' })
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`)
  return res.json()
}

export const fetchUsers = () => api('/users')
export const fetchAccounts = () => api('/accounts')
export const fetchHealth = () => api('/health')
