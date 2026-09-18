import { readFileSync, readdirSync } from 'node:fs'
import { extname, join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const sourceRoot = resolve(process.cwd(), 'src')
const sourceExtensions = new Set(['.js', '.ts', '.vue'])

function listSourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return listSourceFiles(path)
    return sourceExtensions.has(extname(entry.name)) ? [path] : []
  })
}

describe('public frontend repository disclosure', () => {
  it('does not expose the server repository URL', () => {
    const repositoryUrl = ['https://github.com', 'yuns2023', 'saiai-server'].join('/')
    const matches = listSourceFiles(sourceRoot).filter((path) =>
      readFileSync(path, 'utf8').includes(repositoryUrl)
    )

    expect(matches).toEqual([])
  })
})
