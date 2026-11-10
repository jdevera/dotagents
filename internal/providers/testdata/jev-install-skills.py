def install_skills(
    *, global_install: bool, claude: bool, cwd: Path | None = None, home: Path | None = None
) -> dict[str, Any]:
    base = (home or Path.home()) if global_install else (cwd or Path.cwd())
    flavor = ".claude" if claude else ".agents"
    destination_root = base / flavor / "skills"
    installed: list[str] = []
    paths: list[str] = []
    for source in sorted(BUNDLED_SKILLS.iterdir()):
        if not source.is_dir() or not (source / "SKILL.md").is_file():
            continue
        destination = destination_root / source.name
        if destination.exists() and not (destination / INSTALL_MARKER).is_file():
            raise CliError(f"refusing to overwrite unmanaged skill: {destination}")
        destination_root.mkdir(parents=True, exist_ok=True)
        staged = Path(tempfile.mkdtemp(prefix=f".{source.name}-", dir=destination_root))
        try:
            shutil.copytree(source, staged, dirs_exist_ok=True)
            (staged / INSTALL_MARKER).write_text("managed by jev install-skills\n")
            if destination.exists():
                shutil.rmtree(destination)
            staged.replace(destination)
        except OSError as exc:
            shutil.rmtree(staged, ignore_errors=True)
            raise CliError(f"could not install skill: {exc}") from exc
        installed.append(source.name)
        paths.append(str(destination))
    return {
        "ok": True,
        "scope": "global" if global_install else "local",
        "flavor": "claude" if claude else "agents",
        "destination": str(destination_root),
        "installed": installed,
        "paths": paths,
    }
