type Props = {
  version?: string
}

export default function Logo({ version }: Props) {
  return (
    <div className="logo">
      <span className="logo-dot" />
      <span className="logo-text">MIST&nbsp;DRIVE</span>
      {version && (
        <span
          className="muted"
          style={{ marginLeft: '.5rem', fontSize: '0.7rem', letterSpacing: 0 }}
          title="Build version"
        >{version}</span>
      )}
    </div>
  )
}
