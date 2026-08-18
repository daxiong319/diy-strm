import { SERVER_URL } from '@/const'
import { ElMessage } from 'element-plus'

interface DownloadLogFileOptions {
  emptyMessage?: string
  successMessage?: string
  errorPrefix?: string
}

interface ClearLogFileOptions {
  emptyMessage?: string
  successMessage?: string
  errorPrefix?: string
}

export function useLogFileActions() {
  const downloadLogFile = (logPath: string, options: DownloadLogFileOptions = {}) => {
    const normalizedLogPath = logPath.trim()
    if (!normalizedLogPath) {
      ElMessage.error(options.emptyMessage || '日志文件路径为空')
      return
    }

    try {
      const downloadUrl = `${SERVER_URL}/logs/download?path=${encodeURIComponent(normalizedLogPath)}`
      const link = document.createElement('a')
      link.href = downloadUrl
      link.download = normalizedLogPath.split('/').pop() || 'logfile.log'
      link.target = '_blank'
      document.body.appendChild(link)
      link.click()
      setTimeout(() => {
        document.body.removeChild(link)
      }, 100)
      ElMessage.success(options.successMessage || '开始下载日志文件')
    } catch (error) {
      const message = error instanceof Error ? error.message : '未知错误'
      ElMessage.error(`${options.errorPrefix || '下载日志失败'}：${message}`)
    }
  }

  // 服务端清空日志文件（truncate，保留文件本身）
  const clearLogFile = async (logPath: string, options: ClearLogFileOptions = {}) => {
    const normalizedLogPath = logPath.trim()
    if (!normalizedLogPath) {
      ElMessage.error(options.emptyMessage || '日志文件路径为空')
      return false
    }

    try {
      const response = await fetch(
        `${SERVER_URL}/logs/clear?path=${encodeURIComponent(normalizedLogPath)}`,
        {
          method: 'POST',
          credentials: 'include',
        },
      )
      let body: { success?: boolean; error?: string } = {}
      try {
        body = (await response.json()) as typeof body
      } catch {
        // 非 JSON 响应保持空对象
      }
      if (!response.ok || !body?.success) {
        ElMessage.error(`${options.errorPrefix || '清空日志失败'}：${body?.error || `HTTP ${response.status}`}`)
        return false
      }
      ElMessage.success(options.successMessage || '日志已清空')
      return true
    } catch (error) {
      const message = error instanceof Error ? error.message : '未知错误'
      ElMessage.error(`${options.errorPrefix || '清空日志失败'}：${message}`)
      return false
    }
  }

  return {
    downloadLogFile,
    clearLogFile,
  }
}