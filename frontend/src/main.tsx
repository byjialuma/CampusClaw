import React from 'react'
import ReactDOM from 'react-dom/client'
import { createBrowserRouter, Navigate, RouterProvider } from 'react-router-dom'
import LoginPage from './pages/LoginPage'
import HomePage from './pages/HomePage'
import DownloadPage from './pages/DownloadPage'
import './styles.css'

// 下载页为半公开路由：自身只认一次性票据，任何失败都跳登录。
const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  { path: '/download', element: <DownloadPage /> },
  { path: '/', element: <HomePage /> },
  { path: '*', element: <Navigate to="/" replace /> },
])

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>,
)
