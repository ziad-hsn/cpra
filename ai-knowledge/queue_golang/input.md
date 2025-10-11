9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \|
Medium<img src="./k13x5e45.png"
style="width:7.08333in;height:3.86458in" />

> <img src="./vsaksaox.png"
> style="width:0.63542in;height:0.22917in" />[*Openinapp*](https://rsci.app.link/?%24canonical_url=https%3A%2F%2Fmedium.com%2Fp%2Fbe77414abe9e&%7Efeature=LoOpenInAppButton&%7Echannel=ShowPostUnderUser&%7Estage=mobileNavBar&source=post_page---top_nav_layout_nav-----------------------------------------)
> *Signup*
> [*Signin*](https://medium.com/m/signin?operation=login&redirect=https%3A%2F%2Fmedium.com%2F%40danielabatibabatunde1%2Fmastering-queues-in-golang-be77414abe9e&source=post_page---top_nav_layout_nav-----------------------global_nav------------------)
>
> <img src="./npbqv5s5.png"
> style="width:0.33333in;height:0.33333in" /><img src="./y2uvi05c.png"
> style="width:1.16406in;height:0.25347in" /><img src="./edydcw54.png"
> style="width:0.18576in;height:0.18924in" />*Search*
> [*Write*](https://medium.com/m/signin?operation=register&redirect=https%3A%2F%2Fmedium.com%2Fnew-story&source=---top_nav_layout_nav-----------------------new_post_topnav------------------)
>
> <img src="./kqzze5nl.png" style="width:7.5in;height:0.46875in" /><img src="./igaqeeau.png" style="width:7.5in;height:0.46875in" />*Writingisforeveryone.*
> [*<u>RegisterforMediumDay</u>*](https://events.zoom.us/ev/Av7REBItl8l_9abuYg_Iyhrgx4cwt8FEGYhzPou4dCMBDIhOV8ZQ~AmiyQniI6sZwr3sSvUHXWMpdX5wpciIv0a3EWsjOm0kEgiush-6TTsavY_EhDomBRAK8a2foXpncjXcEQADVKgkbMA?source=---medium_day_banner-----------------------------------------)
>
> *MasteringQueuesinGolang*
>
> <img src="./cg23dsty.png"
> style="width:0.33333in;height:0.33333in" />[*AbatiBabatundeDaniel*](https://medium.com/@danielabatibabatunde1?source=post_page---byline--be77414abe9e---------------------------------------)
> *Follow* *11minread* *·* *Jul2,2025*
>
> <img src="./5tbsuoqu.png"
> style="width:0.20833in;height:0.20833in" /><img src="./mkozloud.png"
> style="width:0.15625in;height:0.20312in" /><img src="./qq1ftl4g.png"
> style="width:0.19531in;height:0.21615in" />*26* *1*
>
> So I’ve been learning and working with Queue these past week; it’s
> been a great learning experience so far, I’ve gone from understanding
> how Queues are being implemented in Golang to how they are applied in
> building real world systems.

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
1/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium I made the decision
to share the concepts i’ve learned so far to you, we’d be looking into
the different implementations of Queues (especially in Go), Types of
Queues and even application of Queues, everything you learn from this
article would form the fundamental basis to going out there & crushing
an interview or building effectively using Data structures.

> Queues are data structures that operates based on the FIFO pattern
> (First In — First Out) in order to mange elements in a specific order.
> Think of a real life queue where the first person on the line gets
> served first and the last person has to wait his/her turn. Computer
> Science Professionals in their glory decided to come up with an
> implementation of Queue in different Programming Language hence why we
> are here (almost every Data structure is modeled after a real-world
> activity, relationship or entity, it’s amazing).
>
> In Queues; Elements are inserted from the rear-end and removed from
> the front-end. Queues are inherently suited for scenarios demanding
> ordered sequence processing where the sequence of arrival determines
> the sequence of service. are Below is a diagram showcasing Queues:

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
2/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \|
Medium<img src="./togjheva.png"
style="width:7.08333in;height:3.86458in" />

> A simple implementation in Go looks like this :
>
> // Queue(FIFO - First in First Out) type Queue \[\]int
>
> Core Queue Operations
>
> There are couple of universally defined operations that are associated
> with Queues in order to efficiently manage its elements, almost every
> of the operations occurs at constant time (O(1)), but the Space
> Complexity is directly proportional to the amount of elements in that
> Queue which makes it linear (O(N)), N is the simply the count of
> elements stored in the Queue:
>
> 1\. Enqueue : This involves adding/inserting a new element into the
> rear-end of the Queue (Tail).

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
3/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium 2. Dequeue: This
operation is the total opposite of a queue as it involves the

> removal of an element from the front-end of the queue.
>
> 3\. Peek/Front : This is simply an operation which checks for the
> first element on the queue.
>
> 4\. IsEmpty : This is an operation that checks whether the queue
> contains element(s), returns a boolean value.
>
> 5\. IsFull : This operation simply checks whether the Queue has reach
> its capacity, also returns a boolean value, relevant to Queues with
> Fixed capacity.
>
> However in Golang; Queues are implemented in various type of ways,
> each of them have their own pros & cons, let’s write the code
> implementation together :
>
> 1\. Using Slices : This is the simplest & most common approach to
> implementing a Queue as Go provides a dynamic and flexible foundation.
> It goes like this :
>
> package main
>
> import ( "fmt"
>
> )
>
> // Queue is a type alias for a slice of integers type Queue \[\]int
>
> // Enqueue adds an element to the rear of the queue func (q \*Queue)
> Enqueue(value int) {
>
> \*q = append(\*q, value) }
>
> // Dequeue removes and returns an element from the front of the queue

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
4/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium func (q \*Queue)
Dequeue() (int, error) {

> if q.IsEmpty() {
>
> return 0, fmt.Errorf("empty queue") }
>
> value := (\*q)\[0\]
>
> (\*q)\[0\] = 0 // Zero out the element (optional) \*q = (\*q)\[1:\]
>
> return value, nil }
>
> // CheckFront returns the front element without removing it func (q
> \*Queue) CheckFront() (int, error) {
>
> if q.IsEmpty() {
>
> return 0, fmt.Errorf("empty queue") }
>
> return (\*q)\[0\], nil }
>
> // IsEmpty checks if the queue is empty func (q \*Queue) IsEmpty()
> bool {
>
> return len(\*q) == 0 }
>
> // Size returns the number of elements in the queue func (q \*Queue)
> Size() int {
>
> return len(\*q) }
>
> // PrintQueue displays all elements in the queue func (q \*Queue)
> PrintQueue() {
>
> if q.IsEmpty() { fmt.Println("Queue is empty") return
>
> }
>
> for \_, item := range \*q { fmt.Printf("%d ", item)
>
> } fmt.Println()
>
> }
>
> The downside to using the Slice-approach is that it’s potentially
> memory-inefficient because when elements are removed, it’s underlying
> array (remember that slices are arrays that are capable of expanding &
> shrinking) of the slice might not be immediately released for Go’s
> garbage collection

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
5/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium mechanism, however
in order to mitigate this an optimization algorithm is used where we set
the removed element to a zero value ( we did that above).

> 2\. Using the container/list package: The Authors / Contributors of
> Golang are putting so much effort to making implementing most Go Data
> structures easy and use in building programs seamlessly (gotta give it
> to them). The Go standard library offers the container/list package,
> which provides a robust implementation of a doubly linked list. This
> package is an excellent choice for constructing a queue in Go, as it
> inherently supports efficient queue operations.
>
> package main
>
> import ( "container/list" "fmt"
>
> )
>
> // Queue implemented using container/list: type Queue struct {
>
> data \*list.List }
>
> // NewQueue creates and returns a new Queue func NewQueue() \*Queue {
>
> return &Queue{data: list.New()} }
>
> // Enqueue:
>
> func (q \*Queue) Enqueue(value int) { q.data.PushBack(value)
>
> }
>
> // Dequeue:
>
> func (q \*Queue) Dequeue() (int, error) { if q.IsEmpty() {
>
> return 0, fmt.Errorf("queue is empty") }
>
> front := q.data.Front() q.data.Remove(front)

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
6/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium return
front.Value.(int), nil

> }
>
> // Front :
>
> func (q \*Queue) Front() (int, error) { if q.IsEmpty() {
>
> return 0, fmt.Errorf("queue is empty") }
>
> return q.data.Front().Value.(int), nil }
>
> // IsEmpty :
>
> func (q \*Queue) IsEmpty() bool { return q.data.Len() == 0
>
> }
>
> // Size :
>
> func (q \*Queue) Size() int { return q.data.Len()
>
> }
>
> 1\. Using Channels : Finding out Channels was also another
> implementation of a queue was surprising and even more intriguing
> considering the nature of how it allows you to send & receive values
> into them one at a time (a concurrent safe thread-queue).
>
> Types of Queues
>
> GetAbatiBabatundeDaniel’sstoriesinyourinbox
>
> *JoinMediumforfreetogetupdatesfromthiswriter.*
>
> *Enteryouremail* *Subscribe*
>
> There are several types of Queues specialized to address several
> problems and address distinct functional & performance requirements:

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
7/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium 1. Simple Queue :
Simple (Linear) Queues are the most straightforward

> representation of the Queue Data structure which adheres strictly to
> FIFO pattern with elements added sequentially at the rear and removed
> from the front.While conceptually simple and easy to implement,
> particularly using arrays, this basic form can introduce
> inefficiencies. Applications of this queue in the RealWorld are the
> Task Schedulers (its algorithms) and Message buffering. The problem
> with Linear Queues from experience so far is that in large datasets,
> dequeuing becomes a problem due to having to shift elements on the
> memory.
>
> 2\. Circular Queue: This is an evolved and advanced form of Queues
> which cleverly addresses the inefficiencies of the Linear Queue. The
> Circular Queue Links the last position of the Queue back to the first;
> this process is called “Wrap Around”. A Mechanism that allows for the
> highly efficient reuse of memory slots within the array as elements
> cycle through, thereby filling up spaces vacated by the dequeued
> (removed) elements, The pervasive use of modulo arithmetic (% size) in
> circular queue operations is not merely an implementation detail but a
> fundamental design pattern. This mathematical property enables the
> underlying array to behave as a continuous loop, allowing pointers
> (like front and rear) to wrap around from the end to the beginning. T
> . RealWorld Application for Circular Queues is that they are used in
> Traffic flows Simulation and CPU scheduling.
>
> package main
>
> import( "fmt"
>
> "container/list" )
>
> // Circular Queues:

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
8/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium type CircularQueue
struct {

> data \[\]interface{} capacity int
>
> front int rear int size int
>
> }
>
> // create an instance of the circular queue:
>
> func NewCircularQueue(capacity int) \*CircularQueue{ return
> &CircularQueue{
>
> data: make(\[\]interface{}, capacity), capacity: capacity,
>
> front: 0, rear: -1, size: 0,
>
> } }
>
> // to check if it's full:
>
> func (c \*CircularQueue) IsFull() bool { return c.size == c.capacity
>
> }
>
> // to check if it's empty:
>
> func (c \*CircularQueue) IsEmpty() bool { return c.size == 0
>
> }
>
> // it's Enqueue Operation goes like this:
>
> func (c \*CircularQueue) Enqueue(data interface{} ) (error) { if
> c.IsFull() {
>
> return fmt.Errorf("Circular Queue is Full!") }
>
> c.rear = (c.rear + 1) % c.capacity // to effectively wrap around
> (ensuring the c.data\[c.rear\] = data // insert the data into the last
> position
>
> c.size ++ // increment size of queue
>
> return nil }
>
> func (c \*CircularQueue) Dequeue() (interface{}, error){ if
> c.IsEmpty() {
>
> return nil, fmt.Errorf("Circular Queue is empty!") }
>
> data := c.data\[c.front\] // retrieves the element in front of the
> queue
>
> c.front = (c.front + 1) % c.capacity // updates the front index to
> move to the c.size-- //decrement queue size

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
9/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium

> return data, nil }
>
> func (c \*CircularQueue) Peek() (interface{}, error) { if c.IsEmpty()
> {
>
> return nil, fmt.Errorf("Circular Queue is empty!") }
>
> value := c.data\[c.front\] return value, nil
>
> }
>
> 3\. Priority Queue(PQ): This is a special variant of Queues for which
> each element is assigned a priority value, which means the elements
> are enqueued or dequeued based on their priority value I.e elements
> with the highest priority are removed first irrespective of the
> insertion position (yeah, it doesn’t work like a typical queue),
> however in the case such that they all have equal priority then we
> adore to the FIFO pattern. PQs are used a lot in implementing heaps
> (binary heaps to be specific), they are also used in Dijkstra ’s
> Algorithm (Maths & CS students can relate) and Data Compression.
>
> package main
>
> import( "fmt"
>
> "container/heap" )
>
> // Priority Queues: type Item struct {
>
> value string priority int index int
>
> }

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
10/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium // PQs tend to
implement the Heap interface (container/heap):

> type PriorityQueue \[\]\*Item
>
> // Let's return the length:
>
> func (pq PriorityQueue) Len() int { return len(pq)
>
> }
>
> // we want to return the highest in the queue: func (pq PriorityQueue)
> Less(i, j int) bool {
>
> return pq\[i\].priority \> pq\[j\].priority }
>
> // In the case we intend to swap elements: func (pq PriorityQueue)
> Swap(i, j int) {
>
> pq\[i\], pq\[j\] = pq\[j\], pq\[i\] pq\[i\].index = i
>
> pq\[j\].index = j }
>
> // in the case where we intend to push element: func (pq
> \*PriorityQueue) Push(x interface{}) {
>
> n := len(\*pq);
>
> item := x.(\*Item) // some kind of type assertion
>
> item.index = n // setting the new item position to the queue \*pq =
> append(\*pq, item)
>
> }
>
> // in the case where we intend to remove an element: func (pq
> \*PriorityQueue) Pop() interface{} {
>
> old := \*pq // make a copy of the existing queue n := len(old) //
> calc. it's length
>
> item := old\[n-1\] //reduce it's length old\[n-1\] = nil // avoid
> memory leak item.index = -1
>
> \*pq = old\[0 : n-1\] // remove the first value and move on to the
> next return item
>
> }
>
> // In the case where we need to update an element(item) we use the
> Heap Interfac func(pq \*PriorityQueue) Update(item \*Item, value
> string, priority int) {
>
> item.value = value item.priority = priority heap.Fix(pq, item.index)
>
> }

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
11/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium Importance &
RealWorld Application of Queues

> Queues are ubiquitous in computer science and software engineering,
> underpinning a vast array of systems due to their fundamental ability
> to manage ordered processing.
>
> 1\. Task Scheduling in Operating Systems:
>
> Queues are indispensable components for managing processes and tasks
> within operating systems. They play a critical role in ensuring the
> fair allocation of CPU time, allowing processes to be executed in the
> order they become ready. For instance, the Round-Robin scheduling
> algorithm frequently leverages circular queues to efficiently cycle
> through active tasks, ensuring that all processes receive a fair share
> of CPU time and preventing any single task from monopolizing system
> resources or experiencing starvation.
>
> 1\. Print Spooling
>
> Printers commonly utilize queues to manage incoming print jobs.When
> multiple users submit documents for printing, these jobs are not
> processed simultaneously. Instead, they are stored in a queue and then
> processed sequentially, one by one, in the exact order they were
> received. This orderly management prevents conflicts, ensures that
> documents are printed correctly, and allows multiple users to send
> print jobs without immediate contention for the printing hardware.
>
> 1\. Network Protocols and Data Buffering

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
12/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium In the realm of
computer networking, protocols such as TCP/IP extensively employ queues
to manage and regulate the flow of data packets. For example, data
packets arriving at a network router are temporarily stored in an
internal queue before being transmitted to their intended destination.
This buffering mechanism helps manage network traffic, smooth out data
bursts, and prevent packet loss during periods of congestion.

> 1\. Customer Service Systems
>
> Queues are a highly visible and common feature in customer service
> environments across various industries. This includes call centers,
> banking institutions, and airport operations. In these settings,
> customers or incoming calls are placed into a queue and are served in
> the strict order of their arrival. This approach ensures fairness in
> service delivery, reduces customer wait times, and provides a
> structured and manageable method for handling fluctuating demand.
>
> Now Let’s Solve a common Queue leetcode; Reversing the first K
> elements of a Queue:
>
> // Solving leetcode :
>
> // Reversing the first K elements in a queue: package main
>
> import ( "fmt"
>
> )
>
> // We set up the Queue: type Queue struct {
>
> elements \[\]int }

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
13/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium

> // we set up its operations too:
>
> func (q \*Queue) Enqueue(element int) { q.elements =
> append(q.elements, element)
>
> }
>
> func (q \*Queue) Dequeue() (int, bool) { if len(q.elements) == 0 {
>
> return 0, false }
>
> element := q.elements\[0\] q.elements = q.elements\[1:\]
>
> return element, true }
>
> func (q \*Queue) Front() (int, bool){ if len(q.elements) == 0 {
>
> return 0, false }
>
> element := q.elements\[0\] return element, true
>
> }
>
> func (q \*Queue) Size() int { return len(q.elements)
>
> }
>
> // the function that solves our problem: func (q \*Queue)
> ReverseAtKthValue(k int) {
>
> if k \<= 0 \|\| k \> q.Size() {
>
> return // it's invalid if it's zero or beyond the length of the queue
> }
>
> // then we create a temporary stack object stack := \[\]int{}
>
> // then we push the first elements of K into that stack: for i := 0; i
> \< k; i++ {
>
> // we dequeue the main Queue element, \_ := q.Dequeue()
>
> // while removing the element, we add the removed element to the
> stack: stack = append(stack, element)
>
> }
>
> // Here, we then remove every element from the stack (LIFO) and add
> them back t for len(stack) \> 0 {
>
> element := stack\[len(stack) - 1\];

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
14/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium stack =
stack\[:len(stack) - 1\]

> q.Enqueue(element) }
>
> // Here we move back the untouched element to the reversed queue: for
> i := 0; i \< q.Size()-k; i++ {
>
> element, \_ := q.Dequeue() q.Enqueue(element)
>
> } }
>
> func main() { q := Queue{}
>
> // Enqueue elements 1 through 10 for i := 1; i \<= 10; i++ {
>
> q.Enqueue(i) }
>
> fmt.Println("Original queue:", q.elements)
>
> k := 4 q.ReverseAtKthValue(k)
>
> fmt.Printf("Queue after reversing first %d elements: %v\n", k,
> q.elements) }
>
> Understanding queues extends beyond merely understanding their FIFO
> principle; it encompasses getting acquainted with different types,
> efficient implementations, real-world applications, and the role they
> play in building performant softwares. Queues are fundamental building
> blocks that enable ordered processing, asynchronous communication, and
> robust fault tolerance. A type of Queue not commonly talked about are
> called Double Ended Queues (Deques); a highly flexible and versatile
> data structure that distinguishes itself by allowing the insertion and
> deletion of elements from *both* its front and back ends. It doesn’t
> operate like a Queue alone as it combines both Stack(LIFO) and Queue
> (FIFO) pattern-like characteristics (understandably why it’ not really
> considered as a type by most, as do i). The

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
15/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium upside to learning
these one at a time is that I’ve gotten accustomed to writing go syntax
better & become more exposed to solving more problems & system design
-related bottlenecks.

> Using this medium once again, to let you know that I’m currently
> building a financial intelligence platform that to help investors make
> more money by providing them actionable insights into the financial
> assets & securities in the financial market — basically a unified
> interface that tracks what they have in their portfolio; performs risk
> assessments on them , provides live updates , news surrounding each
> assets and also performs sentimental analytics on the crypto or stocks
> they own in order to help them stay ahead & get huge returns on their
> investments. The name of the platform is DYOR. I have been sharing
> demo updates on the progress of what I’m building on my LinkedIn :
>
> [<u>https://www.linkedin.com/in/abati-daniel-5960b5248?</u>](https://www.linkedin.com/in/abati-daniel-5960b5248?utm_source=share&utm_campaign=share_via&utm_content=profile&utm_medium=android_app)
> [<u>utm_source=share&utm_campaign=share_via&utm_content=</u>p<u>rofile&utm\_</u>](https://www.linkedin.com/in/abati-daniel-5960b5248?utm_source=share&utm_campaign=share_via&utm_content=profile&utm_medium=android_app)
> [<u>medium=android_app</u>](https://www.linkedin.com/in/abati-daniel-5960b5248?utm_source=share&utm_campaign=share_via&utm_content=profile&utm_medium=android_app)
>
> You should definitely check them out and connect with me (I’m in dire
> need of like-minded builders on my timeline). Selah!
>
> <img src="./22a5hdzw.png"
> style="width:1.72917in;height:0.38542in" />[*SoftwareEngineering*](https://medium.com/tag/software-engineering?source=post_page-----be77414abe9e---------------------------------------)
> [*GolangTutorial*](https://medium.com/tag/golang-tutorial?source=post_page-----be77414abe9e---------------------------------------)
> [*DataStructures*](https://medium.com/tag/data-structures?source=post_page-----be77414abe9e---------------------------------------)
> [*Golang*](https://medium.com/tag/golang?source=post_page-----be77414abe9e---------------------------------------)
>
> [*BackendDevelopment*](https://medium.com/tag/backend-development?source=post_page-----be77414abe9e---------------------------------------)

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
16/20

<img src="./rbey1vff.png" style="width:0.5in;height:0.5in" />9/17/25,
4:47 PM Mastering Queues in Golang. So I’ve been learning and working
with… \| by Abati Babatunde Daniel \| Medium
[*WrittenbyAbatiBabatundeDaniel*](https://medium.com/@danielabatibabatunde1?source=post_page---post_author_info--be77414abe9e---------------------------------------)
*Follow*
[*60followers*](https://medium.com/@danielabatibabatunde1/followers?source=post_page---post_author_info--be77414abe9e---------------------------------------)
*·*
[*15following*](https://medium.com/@danielabatibabatunde1/following?source=post_page---post_author_info--be77414abe9e---------------------------------------)

> *I'mgoingtobegreat,that'safactthat'swhyi'dbedocumentinglessons&*
> *experiencesherethroughmyjourneytogreatness.CurrentlyaSoftware*
> *Engineer.*
>
> *Responses(1)*
>
> <img src="./h4qoukzi.png"
> style="width:0.33333in;height:0.33333in" /><img src="./514yzrs2.png"
> style="width:0.33333in;height:0.33333in" />*Writearesponse*
>
> [*Whatareyourthoughts?*](https://medium.com/m/signin?operation=register&redirect=https%3A%2F%2Fmedium.com%2F%40danielabatibabatunde1%2Fmastering-queues-in-golang-be77414abe9e&source=---post_responses--be77414abe9e---------------------respond_sidebar------------------)
>
> <img src="./pawfuqgy.png"
> style="width:0.33333in;height:0.33333in" /><img src="./dwzfg4tx.png"
> style="width:0.15816in;height:0.15885in" />[*PaulHewlett*](https://medium.com/@phewlett76?source=post_page---post_responses--be77414abe9e----0-----------------------------------)
> [*Jul20*](https://medium.com/@phewlett76/why-the-q-in-the-slices-example-that-is-c-777a5cf241cc?source=post_page---post_responses--be77414abe9e----0-----------------------------------)
>
> *Whythe(\*q)intheslicesexample?ThatisC.*
>
> <img src="./rlkhmxwe.png"
> style="width:0.19531in;height:0.21615in" />*1reply* *<u>Reply</u>*
>
> *MorefromAbatiBabatundeDaniel*

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
17/20

<img src="./mdafzb54.png"
style="width:3.39583in;height:2.03125in" /><img src="./rs5s5ahz.png"
style="width:3.39583in;height:1.94792in" /><img src="./0uvg4sjf.png"
style="width:0.20833in;height:0.20833in" /><img src="./pr4dpvmg.png"
style="width:0.20833in;height:0.20833in" /><img src="./hbsiegp5.png"
style="width:0.20833in;height:0.20833in" />9/17/25, 4:47 PM Mastering
Queues in Golang. So I’ve been learning and working with… \| by Abati
Babatunde Daniel \| Medium

> [*AbatiBabatundeDaniel*](https://medium.com/@danielabatibabatunde1?source=post_page---author_recirc--be77414abe9e----0---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
>
> [*WhyGolangmightbeworthit.*](https://medium.com/@danielabatibabatunde1/an-introduction-to-golang-go-programming-language-b4c76d0e20ba?source=post_page---author_recirc--be77414abe9e----0---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
>
> [*AnintroductiontoGolang(Go)&my*](https://medium.com/@danielabatibabatunde1/an-introduction-to-golang-go-programming-language-b4c76d0e20ba?source=post_page---author_recirc--be77414abe9e----0---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
> [*experience*](https://medium.com/@danielabatibabatunde1/an-introduction-to-golang-go-programming-language-b4c76d0e20ba?source=post_page---author_recirc--be77414abe9e----0---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
>
> [*AbatiBabatundeDaniel*](https://medium.com/@danielabatibabatunde1?source=post_page---author_recirc--be77414abe9e----1---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)

[*PointersinGolang*](https://medium.com/@danielabatibabatunde1/pointers-in-golang-240b30c6940d?source=post_page---author_recirc--be77414abe9e----1---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)

[*Istrugglealotwithrememberinghowpointer*](https://medium.com/@danielabatibabatunde1/pointers-in-golang-240b30c6940d?source=post_page---author_recirc--be77414abe9e----1---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
[*worksandeventhesyntaxandthisisnotjus…*](https://medium.com/@danielabatibabatunde1/pointers-in-golang-240b30c6940d?source=post_page---author_recirc--be77414abe9e----1---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)

> <img src="./ye4zvtsg.png"
> style="width:3.39583in;height:1.94792in" /><img src="./bd3xjgel.png"
> style="width:3.39583in;height:2.26042in" /><img src="./xy05a1af.png"
> style="width:0.20833in;height:0.20833in" /><img src="./4lkf2p3n.png"
> style="width:0.20833in;height:0.20833in" /><img src="./annpejhl.png"
> style="width:0.16667in;height:0.20226in" /><img src="./upsl1fcl.png"
> style="width:0.15451in;height:0.12413in" /><img src="./kcygut0h.png"
> style="width:0.16667in;height:0.20226in" />*Jan20* [*190*
> *4*](https://medium.com/@danielabatibabatunde1/an-introduction-to-golang-go-programming-language-b4c76d0e20ba?source=post_page---author_recirc--be77414abe9e----0---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
> *Apr11* [*103*
> *1*](https://medium.com/@danielabatibabatunde1/pointers-in-golang-240b30c6940d?source=post_page---author_recirc--be77414abe9e----1---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
>
> [*AbatiBabatundeDaniel*](https://medium.com/@danielabatibabatunde1?source=post_page---author_recirc--be77414abe9e----2---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
>
> [*SimplifyingStructs,Methodsand*](https://medium.com/@danielabatibabatunde1/simplifying-structs-methods-and-interfaces-in-golang-e86a0c4618aa?source=post_page---author_recirc--be77414abe9e----2---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
> [*InterfacesinGolang.*](https://medium.com/@danielabatibabatunde1/simplifying-structs-methods-and-interfaces-in-golang-e86a0c4618aa?source=post_page---author_recirc--be77414abe9e----2---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
>
> [*Learning,writingandbuildingaprojectwith*](https://medium.com/@danielabatibabatunde1/simplifying-structs-methods-and-interfaces-in-golang-e86a0c4618aa?source=post_page---author_recirc--be77414abe9e----2---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
> [*Golangthispastfewweekshasopenedmy…*](https://medium.com/@danielabatibabatunde1/simplifying-structs-methods-and-interfaces-in-golang-e86a0c4618aa?source=post_page---author_recirc--be77414abe9e----2---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
>
> [*AbatiBabatundeDaniel*](https://medium.com/@danielabatibabatunde1?source=post_page---author_recirc--be77414abe9e----3---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)

[*MasteringLinkedListsinGolang*](https://medium.com/@danielabatibabatunde1/mastering-linked-lists-in-golang-fd080a591533?source=post_page---author_recirc--be77414abe9e----3---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)

> <img src="./b3kqi4h1.png"
> style="width:0.15451in;height:0.12413in" /><img src="./l34vo4mw.png"
> style="width:0.16667in;height:0.20226in" /><img src="./hv5g52hj.png"
> style="width:0.15451in;height:0.12413in" /><img src="./vim0caeh.png"
> style="width:0.16667in;height:0.20226in" />*Mar7* [*5*
> *1*](https://medium.com/@danielabatibabatunde1/simplifying-structs-methods-and-interfaces-in-golang-e86a0c4618aa?source=post_page---author_recirc--be77414abe9e----2---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
> *Jun12* [*117*
> *1*](https://medium.com/@danielabatibabatunde1/mastering-linked-lists-in-golang-fd080a591533?source=post_page---author_recirc--be77414abe9e----3---------------------bd3c0889_e3d8_4574_a05f_e6eabcd51688--------------)
>
> [*SeeallfromAbatiBabatundeDaniel*](https://medium.com/@danielabatibabatunde1?source=post_page---author_recirc--be77414abe9e---------------------------------------)

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
18/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium

> <img src="./f5oi02zu.png"
> style="width:3.39583in;height:1.71875in" /><img src="./f0yozi4l.png"
> style="width:3.39583in;height:3.39583in" /><img src="./4gqhavq0.png"
> style="width:0.20833in;height:0.20833in" /><img src="./5ion3zgi.png"
> style="width:0.20833in;height:0.20833in" />*RecommendedfromMedium*
>
> *In*
> [*Stackademic*](https://medium.com/stackademic?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
> *by*
> [*LukeSloane-Bulger*](https://medium.com/@luke.sloanebulger?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*WhatAreMutexesinGolang?*](https://medium.com/stackademic/what-are-mutexes-in-golang-learn-in-3-minutes-16c196e65e3d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
> [*Learnin3Minutes*](https://medium.com/stackademic/what-are-mutexes-in-golang-learn-in-3-minutes-16c196e65e3d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*Protectingsharedresourcesisessentialfor*](https://medium.com/stackademic/what-are-mutexes-in-golang-learn-in-3-minutes-16c196e65e3d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
> [*buildingconcurrentandefficientapplications.*](https://medium.com/stackademic/what-are-mutexes-in-golang-learn-in-3-minutes-16c196e65e3d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*Hamlet*](https://medium.com/@hamlet_dev?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

[*JSONinGo:TheSilentRevolution*](https://medium.com/@hamlet_dev/json-in-go-the-silent-revolution-youre-about-to-miss-9431494a07a9?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
[*You’reAbouttoMiss*](https://medium.com/@hamlet_dev/json-in-go-the-silent-revolution-youre-about-to-miss-9431494a07a9?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

[*Fromvagueerrorstofloat64nightmares,*](https://medium.com/@hamlet_dev/json-in-go-the-silent-revolution-youre-about-to-miss-9431494a07a9?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
[*JSON/v2fixesthepainpointseveryGo…*](https://medium.com/@hamlet_dev/json-in-go-the-silent-revolution-youre-about-to-miss-9431494a07a9?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

> <img src="./zkkgi0ln.png"
> style="width:3.39583in;height:1.88542in" /><img src="./gych5qxv.png"
> style="width:3.39583in;height:2.26042in" /><img src="./1kcsxtgc.png"
> style="width:0.20833in;height:0.20833in" /><img src="./qxohe5he.png"
> style="width:0.20833in;height:0.20833in" /><img src="./lrkeljex.png"
> style="width:0.20833in;height:0.20833in" /><img src="./largoers.png"
> style="width:0.15451in;height:0.12413in" />*May27*
> [*19*](https://medium.com/stackademic/what-are-mutexes-in-golang-learn-in-3-minutes-16c196e65e3d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
> *Sep* *5*
> [*10*](https://medium.com/@hamlet_dev/json-in-go-the-silent-revolution-youre-about-to-miss-9431494a07a9?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*ObservabilityGuy*](https://medium.com/@observabilityguy?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*WeWroteaGoMicroservice—And*](https://medium.com/@observabilityguy/we-wrote-a-go-microservice-and-it-ate-10gb-of-ram-fa83c26cb77d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
> [*ItAte10GBofRAM*](https://medium.com/@observabilityguy/we-wrote-a-go-microservice-and-it-ate-10gb-of-ram-fa83c26cb77d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*Iopenedthemetricsdashboard,andIswear*](https://medium.com/@observabilityguy/we-wrote-a-go-microservice-and-it-ate-10gb-of-ram-fa83c26cb77d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
> [*myeyesalmostpoppedout—our200MB…*](https://medium.com/@observabilityguy/we-wrote-a-go-microservice-and-it-ate-10gb-of-ram-fa83c26cb77d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*Sanyamdubey*](https://medium.com/@sanyamdubey28?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

[*StopWritingSlowCode:3Go*](https://medium.com/@sanyamdubey28/stop-writing-slow-code-3-go-tricks-to-10x-your-performance-0b191ae24810?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
[*Tricksto10xYourPerformance*](https://medium.com/@sanyamdubey28/stop-writing-slow-code-3-go-tricks-to-10x-your-performance-0b191ae24810?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

[*Go(Golang)isknownforitssimplicity,*](https://medium.com/@sanyamdubey28/stop-writing-slow-code-3-go-tricks-to-10x-your-performance-0b191ae24810?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
[*concurrencymodel,andperformanceoutof…*](https://medium.com/@sanyamdubey28/stop-writing-slow-code-3-go-tricks-to-10x-your-performance-0b191ae24810?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
19/20

9/17/25, 4:47 PM Mastering Queues in Golang. So I’ve been learning and
working with… \| by Abati Babatunde Daniel \| Medium

> <img src="./ddjt5yuk.png"
> style="width:3.39583in;height:2.26042in" /><img src="./ygzx1xvw.png"
> style="width:0.20833in;height:0.20833in" /><img src="./1hkqbcv2.png"
> style="width:3.39583in;height:1.94792in" /><img src="./4xmovsqj.png"
> style="width:0.20833in;height:0.20833in" /><img src="./0xb1ti25.png"
> style="width:0.15451in;height:0.12413in" /><img src="./5w0oghtt.png"
> style="width:0.15451in;height:0.12413in" />*Aug13*
> [*2*](https://medium.com/@observabilityguy/we-wrote-a-go-microservice-and-it-ate-10gb-of-ram-fa83c26cb77d?source=post_page---read_next_recirc--be77414abe9e----0---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
> *May20*
> [*3*](https://medium.com/@sanyamdubey28/stop-writing-slow-code-3-go-tricks-to-10x-your-performance-0b191ae24810?source=post_page---read_next_recirc--be77414abe9e----1---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*QuantumAnomaly*](https://medium.com/@mehul25?source=post_page---read_next_recirc--be77414abe9e----2---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*MemorymanagementinGo*](https://medium.com/@mehul25/memory-in-go-bb3a25ac2f9a?source=post_page---read_next_recirc--be77414abe9e----2---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*MemorySegmentsinGo*](https://medium.com/@mehul25/memory-in-go-bb3a25ac2f9a?source=post_page---read_next_recirc--be77414abe9e----2---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*AbatiBabatundeDaniel*](https://medium.com/@danielabatibabatunde1?source=post_page---read_next_recirc--be77414abe9e----3---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

[*PointersinGolang*](https://medium.com/@danielabatibabatunde1/pointers-in-golang-240b30c6940d?source=post_page---read_next_recirc--be77414abe9e----3---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

[*Istrugglealotwithrememberinghowpointer*](https://medium.com/@danielabatibabatunde1/pointers-in-golang-240b30c6940d?source=post_page---read_next_recirc--be77414abe9e----3---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
[*worksandeventhesyntaxandthisisnotjus…*](https://medium.com/@danielabatibabatunde1/pointers-in-golang-240b30c6940d?source=post_page---read_next_recirc--be77414abe9e----3---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)

> <img src="./j05oiqlk.png"
> style="width:0.15451in;height:0.12413in" /><img src="./hagxe4l5.png"
> style="width:0.15451in;height:0.12413in" /><img src="./3ogfsl10.png"
> style="width:0.16667in;height:0.20226in" />*Apr9*
> [*4*](https://medium.com/@mehul25/memory-in-go-bb3a25ac2f9a?source=post_page---read_next_recirc--be77414abe9e----2---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
> *Apr11* [*103*
> *1*](https://medium.com/@danielabatibabatunde1/pointers-in-golang-240b30c6940d?source=post_page---read_next_recirc--be77414abe9e----3---------------------a8dd0a64_4f16_48b9_9a02_a8fa81f3e3fa--------------)
>
> [*Seemorerecommendations*](https://medium.com/?source=post_page---read_next_recirc--be77414abe9e---------------------------------------)

https://medium.com/@danielabatibabatunde1/mastering-queues-in-golang-be77414abe9e
20/20
