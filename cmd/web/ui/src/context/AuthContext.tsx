import { createContext, useContext, useEffect, useState } from 'react'
import { loginApi, logoutApi, meApi, registerApi } from '../api.ts'
import type { AuthUser } from '../types.ts'

interface AuthCtx {
  user: AuthUser | null
  loading: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  register: (username: string, password: string, confirmPassword: string) => Promise<void>
}

const AuthContext = createContext<AuthCtx>({
  user: null,
  loading: true,
  login: async () => {},
  logout: async () => {},
  register: async () => {},
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

  const register = async (username: string, password: string, confirmPassword: string) => {
    await registerApi(username, password, confirmPassword)
    const data = await meApi()
    setUser({ username: data.username, role: data.role as AuthUser['role'] })
  }

  return <AuthContext.Provider value={{ user, loading, login, logout, register }}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthCtx {
  return useContext(AuthContext)
}
