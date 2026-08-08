export const sourceTypeOptions: Array<Record<string, string>> = [
  {
    label: '115 网盘',
    value: '115',
  },
  {
    label: '123 云盘',
    value: '123',
  },
  {
    label: '光鸭云盘',
    value: 'guangyapan',
  },
  {
    label: '移动云盘',
    value: 'pan139',
  },
  {
    label: '百度网盘',
    value: 'baidupan',
  },
  {
    label: 'OpenList',
    value: 'openlist',
  },
  {
    label: '本地目录',
    value: 'local',
  },
]

export const sourceTypeTagMap: Record<string, string> = {
  '115': 'success',
  '123': 'primary',
  guangyapan: 'warning',
  pan139: 'danger',
  baidupan: 'danger',
  openlist: 'warning',
  local: 'info',
}

export const sourceTypeMap: Record<string, string> = {
  '115': '115 网盘',
  '123': '123 云盘',
  guangyapan: '光鸭云盘',
  pan139: '移动云盘',
  baidupan: '百度网盘',
  openlist: 'OpenList',
  local: '本地目录',
}
