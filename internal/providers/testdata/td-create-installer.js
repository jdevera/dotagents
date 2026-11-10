import { access, mkdir, rmdir, unlink, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import packageJson from '../../../package.json' with { type: 'json' };
import { CliError } from '../errors.js';
import { SKILL_COMPATIBILITY, SKILL_CONTENT, SKILL_DESCRIPTION, SKILL_NAME } from './content.js';
export function generateSkillFile() {
    const frontmatter = `---
name: ${SKILL_NAME}
description: ${JSON.stringify(SKILL_DESCRIPTION)}
compatibility: ${JSON.stringify(SKILL_COMPATIBILITY)}
license: ${packageJson.license}
metadata:
  author: Doist
---

`;
    return frontmatter + SKILL_CONTENT;
}
function isEnoent(error) {
    return error instanceof Error && 'code' in error && error.code === 'ENOENT';
}
/** Removes up to `levels` directories, walking upwards, stopping at the first one that is not empty. */
async function removeEmptyDirs(startDir, levels) {
    let dir = startDir;
    for (let level = 0; level < levels; level++) {
        try {
            await rmdir(dir);
        }
        catch {
            return;
        }
        dir = dirname(dir);
    }
}
export function createInstaller(config) {
    function getInstallPath(local) {
        const base = local ? process.cwd() : homedir();
        const dirName = local ? config.dirName : (config.globalDirName ?? config.dirName);
        return join(base, dirName, 'skills', SKILL_NAME, 'SKILL.md');
    }
    function getLegacyInstallPath(local) {
        if (local || !config.legacyGlobalDirName) {
            return undefined;
        }
        return join(homedir(), config.legacyGlobalDirName, 'skills', SKILL_NAME, 'SKILL.md');
    }
    async function hasLegacyInstall(local) {
        const filepath = getLegacyInstallPath(local);
        if (!filepath) {
            return false;
        }
        try {
            await access(filepath);
            return true;
        }
        catch {
            return false;
        }
    }
    async function removeLegacyInstall(local) {
        const filepath = getLegacyInstallPath(local);
        if (!filepath) {
            return;
        }
        try {
            await unlink(filepath);
        }
        catch (error) {
            // Anything other than the file already being gone leaves the old skill
            // active for clients that still read it, so it must not pass silently.
            if (isEnoent(error)) {
                return;
            }
            throw error;
        }
        // Prune the now-empty todoist-cli/ directory and, if nothing else lives
        // there, the skills/ directory that contained it.
        await removeEmptyDirs(dirname(filepath), 2);
    }
    return {
        name: config.name,
        description: config.description,
        getInstallPath,
        getLegacyInstallPath,
        hasLegacyInstall,
        generateContent() {
            return generateSkillFile();
        },
        async isInstalled(local) {
            try {
                await access(getInstallPath(local));
                return true;
            }
            catch {
                return false;
            }
        },
        async install(local, force) {
            if (!local && config.dirName !== '.agents') {
                const agentDir = join(homedir(), config.dirName);
                try {
                    await access(agentDir);
                }
                catch {
                    throw new CliError('NOT_INSTALLED', `${config.name} does not appear to be installed (${agentDir} not found)`);
                }
            }
            const filepath = getInstallPath(local);
            const exists = await this.isInstalled(local);
            if (exists && !force) {
                throw new CliError('ALREADY_EXISTS', `Skill file already exists at ${filepath}. Use --force to overwrite.`);
            }
            await mkdir(dirname(filepath), { recursive: true });
            await writeFile(filepath, this.generateContent(), 'utf-8');
            await removeLegacyInstall(local);
        },
        async update(local) {
            const filepath = getInstallPath(local);
            await mkdir(dirname(filepath), { recursive: true });
            await writeFile(filepath, this.generateContent(), 'utf-8');
            await removeLegacyInstall(local);
        },
        async uninstall(local) {
            const filepath = getInstallPath(local);
            const exists = await this.isInstalled(local);
            const legacyExists = await hasLegacyInstall(local);
            if (!exists && !legacyExists) {
                throw new CliError('NOT_FOUND', `Skill file not found at ${filepath}`);
            }
            if (exists) {
                await unlink(filepath);
                await removeEmptyDirs(dirname(filepath), 1);
            }
            await removeLegacyInstall(local);
        },
    };
}
//# sourceMappingURL=create-installer.js.map