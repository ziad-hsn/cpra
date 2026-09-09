export function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="error-state" role="alert">
      <span style={{ fontSize: 16, fontWeight: 600, color: 'var(--status-critical)' }}>
        ⚠ Error
      </span>
      <span style={{ fontSize: 12 }}>{message}</span>
      {onRetry && (
        <button onClick={onRetry} style={{ marginTop: 4 }}>
          Retry
        </button>
      )}
    </div>
  );
}
