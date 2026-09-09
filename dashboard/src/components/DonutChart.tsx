interface DonutSegment {
  label: string;
  value: number;
  color: string;
}

interface DonutChartProps {
  segments: DonutSegment[];
  size?: number;
  thickness?: number;
  centerLabel?: string;
  centerValue?: string | number;
  ariaLabel?: string;
}

export function DonutChart({
  segments,
  size = 180,
  thickness = 20,
  centerLabel = '',
  centerValue = '',
  ariaLabel = 'donut chart',
}: DonutChartProps) {
  const total = segments.reduce((s, seg) => s + seg.value, 0);
  const radius = (size - thickness) / 2;
  const cx = size / 2;
  const cy = size / 2;
  const circumference = 2 * Math.PI * radius;
  const GAP = 2; // px gap between arcs

  if (total === 0) {
    return (
      <div className='col' style={{ alignItems: 'center' }}>
        <svg width={size} height={size} role='img' aria-label={ariaLabel + ': no data'}>
          <circle cx={cx} cy={cy} r={radius} fill='none' stroke='var(--surface-2)' strokeWidth={thickness} />
        </svg>
        <span className='muted' style={{ fontSize: 12 }}>No data</span>
      </div>
    );
  }

  let offset = 0;
  const arcs = segments
    .filter((seg) => seg.value > 0)
    .map((seg) => {
      const fraction = seg.value / total;
      const dash = Math.max(0, fraction * circumference - GAP);
      const arc = { ...seg, dash, gap: circumference - dash, offset: -offset };
      offset += fraction * circumference;
      return arc;
    });

  return (
    <div className='row' style={{ gap: 20, flexWrap: 'wrap' }}>
      <svg width={size} height={size} role='img' aria-label={ariaLabel} style={{ filter: 'drop-shadow(0 4px 12px rgba(0,0,0,0.4))' }}>
        <g transform={'rotate(-90 ' + cx + ' ' + cy + ')'}>
          {arcs.map((arc, i) => (
            <circle
              key={i}
              cx={cx}
              cy={cy}
              r={radius}
              fill='none'
              stroke={arc.color}
              strokeWidth={thickness}
              strokeLinecap='round'
              strokeDasharray={arc.dash + ' ' + arc.gap}
              strokeDashoffset={arc.offset}
              style={{ filter: 'drop-shadow(0 0 6px ' + arc.color + ')' }}
            />
          ))}
        </g>
        {centerValue !== '' && (
          <text x={cx} y={cy - 2} textAnchor='middle' fill='var(--text-primary)' fontSize={size * 0.22} fontWeight={700}>
            {centerValue}
          </text>
        )}
        {centerLabel && (
          <text x={cx} y={cy + size * 0.15} textAnchor='middle' fill='var(--text-muted)' fontSize={11} letterSpacing='0.5'>
            {centerLabel}
          </text>
        )}
      </svg>
      <ul style={{ listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6, margin: 0, padding: 0 }}>
        {segments.map((seg, i) => (
          <li key={i} className='row' style={{ gap: 8, fontSize: 12.5 }}>
            <span className='status-dot' style={{ background: seg.color, boxShadow: '0 0 8px ' + seg.color }} />
            <span className='muted'>{seg.label}</span>
            <span style={{ fontWeight: 600, marginLeft: 'auto', fontVariantNumeric: 'tabular-nums' }}>{seg.value}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
