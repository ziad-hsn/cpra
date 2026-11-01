import { Component, type ReactNode } from 'react';

/** Only route content is replaced. Authentication, caches and navigation remain
 * owned by the surrounding shell; error objects never become rendered output. */
type Props = {children: ReactNode; fallback: ReactNode; resetKey: string};
export class PageBoundary extends Component<Props, {failed: boolean; resetKey: string}> {
  state = {failed: false, resetKey: this.props.resetKey};
  static getDerivedStateFromProps(props: Props, state: {resetKey: string}) {
    // Navigation clears a failed route without remounting healthy children.
    // In particular, a successful save must retain its receipt after navigation.
    return props.resetKey === state.resetKey ? null : {failed: false, resetKey: props.resetKey};
  }
  static getDerivedStateFromError() { return {failed: true}; }
  render() { return this.state.failed ? this.props.fallback : this.props.children; }
}
