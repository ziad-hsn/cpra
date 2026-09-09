export function LoadingSkeleton({ width = '100%', height = 20, lines = 3 }: { width?: string; height?: number; lines?: number }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-busy="true">
      {Array.from({ length: lines }).map((_, i) => (
        <div
          key={i}
          className="skeleton"
          style={{ width: i === lines - 1 ? '60%' : width, height }}
        />
      ))}
    </div>
  );
}
