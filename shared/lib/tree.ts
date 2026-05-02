export interface FileObject {
  key: string
  size: number
  lastModified?: string
}

export type TreeNode = {
  name: string
  path: string
  isDir: boolean
  size: number
  lastModified?: string
  children?: Record<string, TreeNode>
}

export function buildTree(files: FileObject[]): TreeNode {
  const root: TreeNode = { name: '', path: '', isDir: true, size: 0, children: {} }
  for (const f of files) {
    const parts = f.key.split('/').filter(Boolean)
    let node = root
    for (let i = 0; i < parts.length; i++) {
      const leaf = i === parts.length - 1
      const name = parts[i]
      if (leaf && name === '.keep') break
      node.children = node.children || {}
      if (!node.children[name]) {
        node.children[name] = {
          name,
          path: parts.slice(0, i + 1).join('/'),
          isDir: !leaf,
          size: 0,
          children: leaf ? undefined : {},
        }
      }
      node = node.children[name]
      if (leaf) {
        node.size = f.size
        node.lastModified = f.lastModified
      }
    }
  }
  const sum = (n: TreeNode): number => {
    if (!n.isDir) return n.size
    let s = 0
    for (const c of Object.values(n.children || {})) s += sum(c)
    n.size = s
    return s
  }
  sum(root)
  return root
}

export function sortedChildren(n: TreeNode): TreeNode[] {
  return Object.values(n.children || {}).sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}
