import { createContext, useContext, useEffect, useState } from 'react'
import { loginApi, logoutApi, meApi } from '../api.ts'
import type { AuthUser } from '../types.ts'

interface AuthCtx {
  user: AuthUser | null
  loading: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthCtx>({
  user: null,
  loading: true,
  login: async () => {},
  logout: async () => {},
})

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    meApi()
      .then((data) => setUser({ username: data.username, role: data.role as AuthUser['role'] }))
      .catch(() => setUser(null))
      .finally(() => setLoading(false))
  }, [])

  const login = async (username: string, password: string) => {
    const data = await loginApi(username, password)
    setUser({ username: data.username, role: data.role as AuthUser['role'] })
  }

  const logout = async () => {
    await logoutApi()
    setUser(null)
  }

  return <AuthContext.Provider value={{ user, loading, login, logout }}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthCtx {
  return useContext(AuthContext)
}
