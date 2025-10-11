<img src="./21ng2pre.png"
style="width:0.52083in;height:0.52083in" /><img src="./2b5va1ur.png"
style="width:0.9783in;height:0.18056in" /><img src="./ynbt3ecs.png"
style="width:0.72656in;height:0.12594in" /><img src="./qq0yuxmj.png" style="width:0.19271in" /><img src="./2mgeivsg.png" style="width:0.22743in" /><img src="./4pqr2n4a.png"
style="width:0.10937in;height:0.12153in" /><img src="./bremwsjc.png"
style="width:0.31684in;height:0.1276in" />

**Little's** **law**

In mathematical [queueing theor
,](https://en.wikipedia.org/wiki/Queueing_theory) **Little's** **law**
(also **result**, **theorem**, **lemma**, or **formula**\[1\]\[2\]) is a
theorem by [John
Little](https://en.wikipedia.org/wiki/John_Little_(academic)) which
states that the long-term average number *L* of customers in a
[stationary](https://en.wikipedia.org/wiki/Stationary_process) system is
equal to the long-term average effective arrival rate *λ* multiplied by
the average time *W* that a customer spends in the system. Expressed
algebraically the law is

The relationship is not influenced by the arrival process distribution,
the service distribution, the service order, or practically anything
else. In most queuing systems, service time is the
[bottleneck](https://en.wikipedia.org/wiki/Bottleneck_(engineering))
that creates the queue.\[3\]

The result applies to any system, and particularly, it applies to
systems within systems.\[4\] For example in a bank branch, the [customer
line](https://en.wikipedia.org/wiki/Queue_area) might be one subsystem,
and each of the [tellers](https://en.wikipedia.org/wiki/Bank_teller)
another subsystem, and Little's result could be applied to each one, as
well as the whole thing. The only requirement is that the system be
[ergodic](https://en.wikipedia.org/wiki/Ergodicity)\[5\].

In some cases it is possible not only to mathematically relate the
*average* number in the system to the

*average* wait but even to relate the entire [*probability*
*distribution*](https://en.wikipedia.org/wiki/Probability_distribution)
(and moments) of the number in the system to the wait.\[6\]

**History**

In a 1954 paper, Little's law was assumed true and used without
proof.\[7\]\[8\] The form *L* = *λW* was first

published by [Philip M.
Morse](https://en.wikipedia.org/wiki/Philip_M._Morse) where he
challenged readers to find a situation where the relationship did not
hold.\[7\]\[9\] Little published in 1961 his proof of the law, showing
that no such situation existed.\[10\] Little's proof was followed by a
simpler version by Jewell\[11\] and another by Eilon.\[12\] Shaler
Stidham published a different and more intuitive proof in
1972.\[13\]\[14\]

**Examples**

**Finding** **response** **time**

Imagine an application that had no easy way to measure [response
time.](https://en.wikipedia.org/wiki/Response_time_(technology)) If the
mean number in the system and the throughput are known, the average
response time can be found using Little’s Law:

> mean response time = mean number in system / mean throughput

For example: A queue depth meter shows an average of nine jobs waiting
to be serviced. Add one for the job being serviced, so there is an
average of ten jobs in the system. Another meter shows a mean throughput
of 50 per second. The mean response time is calculated as 0.2 seconds =
10 / 50 per second.

**Customers** **in** **the** **store**<img src="./25bx2pmi.png"
style="width:0.10937in;height:0.12153in" /><img src="./3b5zffjn.png"
style="width:0.28038in;height:0.1276in" /><img src="./ac3fvxac.png"
style="width:0.15451in;height:0.1224in" /><img src="./ua5cpccf.png" /><img src="./2j2y1ndv.png"
style="width:0.21007in;height:0.1224in" /><img src="./mqlcngxp.png" style="height:0.1224in" /><img src="./qznfisvk.png"
style="width:0.17708in;height:0.12413in" /><img src="./n4qrrocd.png"
style="width:0.10937in;height:0.12153in" /><img src="./fkyfvi5j.png" style="height:0.125in" /><img src="./sq23m4zi.png" style="height:0.11719in" /><img src="./jppz1oql.png"
style="width:0.15538in;height:0.12153in" /><img src="./o2bw2msv.png"
style="width:0.21181in;height:0.12153in" />

Imagine a small store with a single counter and an area for browsing,
where only one person can be at the counter at a time, and no one leaves
without buying something. So the system is:

> *entrance* *→* *browsing* *→* *counter* *→* *exit*

If the rate at which people enter the store (called the arrival rate) is
the rate at which they exit (called the exit rate), the system is
stable. By contrast, an arrival rate exceeding an exit rate would
represent an unstable system, where the number of waiting customers in
the store would gradually increase towards infinity.

Little's Law tells us that the average number of customers in the store
*L*, is the effective arrival rate *λ*, times the average time that a
customer spends in the store *W*, or simply:

Assume customers arrive at the rate of 10 per hour and stay an average
of 0.5 hour. This means we should find the average number of customers
in the store at any time to be 5.

Now suppose the store is considering doing more advertising to raise the
arrival rate to 20 per hour. The store must either be prepared to host
an average of 10 occupants or must reduce the time each customer spends
in the store to 0.25 hour. The store might achieve the latter by ringing
up the bill faster or by adding more counters.

We can apply Little's Law to systems within the store. For example,
consider the counter and its queue. Assume we notice that there are on
average 2 customers in the queue and at the counter. We know the arrival
rate is 10 per hour, so customers must be spending 0.2 hours on average
checking out.

We can even apply Little's Law to the counter itself. The average number
of people at the counter would be in the range (0, 1) since no more than
one person can be at the counter at a time. In that case, the average
number of people at the counter is also known as the utilisation of the
counter.

However, because a store in reality generally has a limited amount of
space, it can eventually become unstable. If the arrival rate is much
greater than the exit rate, the store will eventually start to overflow,
and thus any new arriving customers will simply be rejected (and forced
to go somewhere else or try again later) until there is once again free
space available in the store. This is also the difference between the
*arrival* *rate* and the *effective* *arrival* *rate*, where the arrival
rate roughly corresponds to the rate at which customers arrive at the
store, whereas the effective arrival rate corresponds to the rate at
which customers *enter* the store. However, in a system with an infinite
size and no loss, the two are equal.

**Estimating** **parameters**

To use Little's law on data, formulas must be used to estimate the
parameters, as the result does not necessarily directly apply over
finite time intervals, due to problems like how to log customers already
present at the start of the logging interval and those who have not yet
departed when logging stops.\[15\]

**Applications**

Little's law is widely used in manufacturing to predict lead time based
on the production rate and the amount of work-in-process.\[16\]

Software-performance testers have used Little's law to ensure that the
observed performance results are not due to bottlenecks imposed by the
testing apparatus. \[17\]\[18\]

Other applications include staffing emergency departments in
hospitals.\[19\]\[20\]

Lastly, an equivalent version of Little's law also applies in the fields
of [demography](https://en.wikipedia.org/wiki/Demography) and
[population](https://en.wikipedia.org/wiki/Population_biology) [biolog
,](https://en.wikipedia.org/wiki/Population_biology) although not
referred to as "Little's Law".\[21\]\[22\] For example, Cohen
(2008)\[23\] explains that in

<img src="./gxxli55u.png"
style="width:0.12674in;height:0.12153in" /><img src="./uxtizzyl.png"
style="width:0.1276in;height:0.12153in" /><img src="./dfldd4rz.png" /><img src="./qhhd2s5b.png"
style="width:0.12934in;height:0.12153in" /><img src="./odmp5bww.png"
style="width:0.12847in;height:0.12153in" /><img src="./25tyddpo.png"
style="width:0.12674in;height:0.12153in" /><img src="./3mcshqyb.png"
style="width:0.1276in;height:0.12153in" /><img src="./z5hcx2m1.png" />a
homogeneous stationary population without migration, , where is the
total population size, is the number of births per year, and is the life
expectancy from birth. The formula

<img src="./zmp3zdrv.png" style="height:0.12587in" /><img src="./1d05fssy.png" /><img src="./fjesuri3.png"
style="width:0.17708in;height:0.125in" />is thus directly equivalent to
Little's law ( ). However, biological populations tend to be dynamic and
therefore more complicated to model accurately.\[24\]

**Distributional** **form**

An extension of Little's law provides a relationship between the steady
state distribution of number of

customers in the system and time spent in the system under a [first
come, first
served](https://en.wikipedia.org/wiki/First_come,_first_served) service
discipline.\[25\]

**See** **also**

> [List of eponymous
> laws](https://en.wikipedia.org/wiki/List_of_eponymous_laws) (laws,
> adages, and other succinct observations or predictions named after
> persons)
>
> [Erlang (unit)](https://en.wikipedia.org/wiki/Erlang_(unit))

**References**

> 1\. Alberto Leon-Garcia (2008). *Probability,* *statistics,* *and*
> *random* *processes* *for* *electrical* *engineering* (3rd ed.).
> Prentice Hall. [ISBN](https://en.wikipedia.org/wiki/ISBN_(identifier))
> [978-0-13-147122-1](https://en.wikipedia.org/wiki/Special:BookSources/978-0-13-147122-1).
>
> 2\. Allen, Arnold A. (1990). [*Probability,* *Statistics,* *and*
> *Queueing* *Theory:* *With*
> *Computer*](https://archive.org/details/probabilitystati0000alle/page/259)
> [*Science* *Applications*
> (https://archive.org/details/probabilitystati0000alle/page/259)](https://archive.org/details/probabilitystati0000alle/page/259).
> Gulf Professional Publishing. p. [259
> (https://archive.org/details/probabilitystati0000alle/page/25](https://archive.org/details/probabilitystati0000alle/page/259)
> [9).](https://archive.org/details/probabilitystati0000alle/page/259)
> [ISBN](https://en.wikipedia.org/wiki/ISBN_(identifier))
> [0120510510.](https://en.wikipedia.org/wiki/Special:BookSources/0120510510)
>
> 3\. Simchi-Levi, D.; [Trick, M.
> A.](https://en.wikipedia.org/wiki/Michael_Trick) (2013). "Introduction
> to "Little's Law as Viewed on Its 50th Anniversary"". *Operations*
> *Research*. **59** (3): 535.
> [doi](https://en.wikipedia.org/wiki/Doi_(identifier)):[10.1287/opre.1110.0941
> (https://doi.or](https://doi.org/10.1287%2Fopre.1110.0941)
> [g/10.1287%2Fopre.1110.0941)](https://doi.org/10.1287%2Fopre.1110.0941).
>
> 4\. Serfozo, R. (1999). "Little Laws". *Introduction* *to*
> *Stochastic* *Networks*. pp. 135–154.
> [doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1007/978-1-4612-1482-3_5
> (https://doi.org/10.1007%2F978-1-4612-1482-3_5).](https://doi.org/10.1007%2F978-1-4612-1482-3_5)
> [ISBN](https://en.wikipedia.org/wiki/ISBN_(identifier))
> [978-1-4612-7160-4](https://en.wikipedia.org/wiki/Special:BookSources/978-1-4612-7160-4).
>
> 5\. Harchol-Balter, Mor. "Chapter 6: Little's Law and Other
> Operational Laws". *Performance* *Modeling* *and* *Design* *of*
> *Computer* *Systems:* *Queueing* *Theory* *in* *Action*. Cambridge
> University Press. p. 98.
>
> 6\. [Keilson, J.](https://en.wikipedia.org/wiki/Julian_Keilson);
> Servi, L. D. (1988). ["A distributional form of Little's Law"
> (https://dspace.mit.edu/](https://dspace.mit.edu/bitstream/1721.1/47244/1/distributionalfo00keil.pdf)
> [bitstream/1721.1/47244/1/distributionalfo00keil.pdf)](https://dspace.mit.edu/bitstream/1721.1/47244/1/distributionalfo00keil.pdf)
> (PDF). *Operations* *Research* *Letters*. **7** (5): 223.
> [doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1016/0167-6377(88)90035-1
> (https://doi.org/10.1016%2F0167-6377%288](https://doi.org/10.1016%2F0167-6377%2888%2990035-1)
> [8%2990035-1).](https://doi.org/10.1016%2F0167-6377%2888%2990035-1)
> [hdl](https://en.wikipedia.org/wiki/Hdl_(identifier))[:1721.1/5305
> (https://hdl.handle.net/1721.1%2F5305).](https://hdl.handle.net/1721.1%2F5305)
>
> 7\. [Little, J. D.
> C.;](https://en.wikipedia.org/wiki/John_Little_(academic)) Graves, S.
> C. (2008). ["Little's Law"
> (http://web.mit.edu/sgraves/www/papers/L](http://web.mit.edu/sgraves/www/papers/Little's%20Law-Published.pdf)
> [ittle's%20Law-Published.pdf)](http://web.mit.edu/sgraves/www/papers/Little's%20Law-Published.pdf)
> (PDF). *Building* *Intuition*. International Series in Operations
> Research & Management Science. Vol. 115. p. 81.
> [doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1007/978-0-387-73699-0_5
> (http](https://doi.org/10.1007%2F978-0-387-73699-0_5)
> [s://doi.org/10.1007%2F978-0-387-73699-0_5)](https://doi.org/10.1007%2F978-0-387-73699-0_5).
> [ISBN](https://en.wikipedia.org/wiki/ISBN_(identifier))
> [978-0-387-73698-3.](https://en.wikipedia.org/wiki/Special:BookSources/978-0-387-73698-3)
>
> 8\. Cobham, Alan (1954). "Priority Assignment in Waiting Line
> Problems". *Operations* *Research*. **2** (1): 70–76.
> [doi:](https://en.wikipedia.org/wiki/Doi_(identifier))[10.1287/opre.2.1.70
> (https://doi.org/10.1287%2Fopre.2.1.70)](https://doi.org/10.1287%2Fopre.2.1.70).
> [JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [166539
> (https://www.jstor.org/stable/166539).](https://www.jstor.org/stable/166539)
>
> 9\. [Morse, Philip M.](https://en.wikipedia.org/wiki/Philip_M._Morse)
> (1958). *Queues,* *inventories,* *and* *maintenance:* *the* *analysis*
> *of* *operational* *system* *with* *variable* *demand* *and* *supply*.
> Wiley. "Those readers who would like to experience for themselves the
> slipperiness of fundamental concepts in this field and the
> intractability of really general theorems, might try their hand at
> showing under what circumstances this simple relationship between L
> and W does not hold."

10\. [Little, J. D.
C.](https://en.wikipedia.org/wiki/John_Little_(academic)) (1961). "A
Proof for the Queuing Formula: *L* = *λW*". [*Operations*
*Research*](https://en.wikipedia.org/wiki/Operations_Research_(journal)).
**9** (3): 383–387.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1287/opre.9.3.383
(https://doi.org/10.1287%2Fopre.9.3.383)](https://doi.org/10.1287%2Fopre.9.3.383).
[JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [167570
(https://www.jstor.org/stable/167570).](https://www.jstor.org/stable/167570)

11\. Jewell, William S. (1967). "A Simple Proof of: *L* = *λW*".
*Operations* *Research*. **15** (6): 1109– 1116.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier)):[10.1287/opre.15.6.1109
(https://doi.org/10.1287%2Fopre.15.6.1109).](https://doi.org/10.1287%2Fopre.15.6.1109)

> [JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [168616
> (https://www.jstor.org/stable/168616).](https://www.jstor.org/stable/168616)

12\. Eilon, Samuel (1969). ["A Simpler Proof of *L* = *λW*"
(https://doi.org/10.1287%2Fopre.17.5.91](https://doi.org/10.1287%2Fopre.17.5.915)
[5).](https://doi.org/10.1287%2Fopre.17.5.915) *Operations* *Research*.
**17** (5): 915–917.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1287/opre.17.5.915
(https://doi.org/10.128](https://doi.org/10.1287%2Fopre.17.5.915)
[7%2Fopre.17.5.915)](https://doi.org/10.1287%2Fopre.17.5.915).
[JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [168368
(https://www.jstor.org/stable/168368).](https://www.jstor.org/stable/168368)

13\. Stidham Jr., Shaler (1974). ["A Last Word on *L* = *λW*"
(https://doi.org/10.1287%2Fopre.22.2.4](https://doi.org/10.1287%2Fopre.22.2.417)
[17).](https://doi.org/10.1287%2Fopre.22.2.417) *Operations* *Research*.
**22** (2): 417–421.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1287/opre.22.2.417
(https://doi.org/10.12](https://doi.org/10.1287%2Fopre.22.2.417)
[87%2Fopre.22.2.417)](https://doi.org/10.1287%2Fopre.22.2.417).
[JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [169601
(https://www.jstor.org/stable/169601).](https://www.jstor.org/stable/169601)

14\. Stidham Jr., Shaler (1972). "*L* = *λW*: A Discounted Analogue and
a New Proof". *Operations* *Research*. **20** (6): 1115–1120.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1287/opre.20.6.1115
(https://doi.org/10.1287%2Fopre.](https://doi.org/10.1287%2Fopre.20.6.1115)
[20.6.1115)](https://doi.org/10.1287%2Fopre.20.6.1115).
[JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [169301
(https://www.jstor.org/stable/169301)](https://www.jstor.org/stable/169301).

15\. Kim, S. H.; [Whitt, W.](https://en.wikipedia.org/wiki/Ward_Whitt)
(2013). ["Statistical Analysis with Little's Law"
(http://www.columbia.edu/](http://www.columbia.edu/~ww2040/LL_OR.pdf)
[~ww2040/LL_OR.pdf)](http://www.columbia.edu/~ww2040/LL_OR.pdf) (PDF).
*Operations* *Research*. **61** (4): 1030.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1287/opre.2013.1193
(https://doi.org/10.1287%2Fopre.2013.1193)](https://doi.org/10.1287%2Fopre.2013.1193).

16\. Correll, Nikolaus (June 13, 2021). ["Manufacturing Lead Time"
(https://thesevendeadlywaste](https://thesevendeadlywastes.com/lesson/lead-time/)
[s.com/lesson/lead-time/).](https://thesevendeadlywastes.com/lesson/lead-time/)
Retrieved June 12, 2021.

17\. [Software Infrastructure Bottlenecks in J2EE by Deepak Goel
(http://www.onjava.com/pub/a/](http://www.onjava.com/pub/a/onjava/2005/01/19/j2ee-bottlenecks.html)
[onjava/2005/01/19/j2ee-bottlenecks.html)](http://www.onjava.com/pub/a/onjava/2005/01/19/j2ee-bottlenecks.html)

18\. [Benchmarking Blunders and Things That Go Bump in the Night by Neil
Gunther (https://arxi](https://arxiv.org/abs/cs/0404043)
[v.org/abs/cs/0404043)](https://arxiv.org/abs/cs/0404043)

19\. [Little, J. D.
C.](https://en.wikipedia.org/wiki/John_Little_(academic)) (2011).
["Little's Law as Viewed on Its 50th Anniversary"
(http://www.informs.or](http://www.informs.org/content/download/255808/2414681/file/little_paper.pdf)
[g/content/download/255808/2414681/file/little_paper.pdf)](http://www.informs.org/content/download/255808/2414681/file/little_paper.pdf)
(PDF). *Operations* *Research*. **59** (3): 536–549.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1287/opre.1110.0940
(https://doi.org/10.1287%2Fopre.1110.0940).](https://doi.org/10.1287%2Fopre.1110.0940)
[JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [23013126
(https://www.jstor.org/stable/23013126)](https://www.jstor.org/stable/23013126).<img src="./eruethig.png" />

20\. Harris, Mark (February 22, 2010). ["Little's Law: The Science
Behind Proper Staffing"
(https://](https://web.archive.org/web/20120905100132/http://www.epmonthly.com/subspecialties/management/littles-law-the-science-behind-proper-staffing/)
[web.archive.org/web/20120905100132/http://www.epmonthly.com/subspecialties/managem](https://web.archive.org/web/20120905100132/http://www.epmonthly.com/subspecialties/management/littles-law-the-science-behind-proper-staffing/)
[ent/littles-law-the-science-behind-proper-staffing/).](https://web.archive.org/web/20120905100132/http://www.epmonthly.com/subspecialties/management/littles-law-the-science-behind-proper-staffing/)
Emergency Physicians Monthly. Archived from [the original
(https://www.epmonthly.com/subspecialties/management/littles-law-the-scie](https://www.epmonthly.com/subspecialties/management/littles-law-the-science-behind-proper-staffing/)
[nce-behind-proper-staffing/)](https://www.epmonthly.com/subspecialties/management/littles-law-the-science-behind-proper-staffing/)
on September 5, 2012. Retrieved September 4, 2012.

21\. Liang, Haili; Guo, Zhen; Tuljapurkar, Shripad (2023). ["Why life
expectancy over-predicts](https://doi.org/10.1186%2Fs41118-023-00188-8)
[crude death rate"
(https://doi.org/10.1186%2Fs41118-023-00188-8).](https://doi.org/10.1186%2Fs41118-023-00188-8)
*Genus*. **79** (1): 9.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1186/s41118-023-00188-8
(https://doi.org/10.1186%2Fs41118-023-00188-8)](https://doi.org/10.1186%2Fs41118-023-00188-8).
[ISSN](https://en.wikipedia.org/wiki/ISSN_(identifier)) [2035-5556
(https://search.worldcat.org/issn/2035-5556).](https://search.worldcat.org/issn/2035-5556)

22\. Murray, Bertram G. (2003). ["A new equation relating population
size and demographic](https://www.jstor.org/stable/23736503)
[parameters: some ecological implications"
(https://www.jstor.org/stable/23736503).](https://www.jstor.org/stable/23736503)
*Annales* *Zoologici* *Fennici*. **40** (6): 465–472.
[ISSN](https://en.wikipedia.org/wiki/ISSN_(identifier)) [0003-455X
(https://search.worldcat.org/issn/0003-455X)](https://search.worldcat.org/issn/0003-455X).
[JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [23736503
(https://www.jstor.org/stable/23736503)](https://www.jstor.org/stable/23736503).

23\. Cohen, Joel E. (2008). ["Constant global population with
demographic heterogeneity"
(http](https://www.demographic-research.org/articles/volume/18/14)
[s://www.demographic-research.org/articles/volume/18/14).](https://www.demographic-research.org/articles/volume/18/14)
*Demographic* *Research*. **18**: 409–436.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier)):[10.4054/DemRes.2008.18.14
(https://doi.org/10.4054%2FDemRes.2008.18.1](https://doi.org/10.4054%2FDemRes.2008.18.14)
[4).](https://doi.org/10.4054%2FDemRes.2008.18.14)
[ISSN](https://en.wikipedia.org/wiki/ISSN_(identifier)) [1435-9871
(https://search.worldcat.org/issn/1435-9871).](https://search.worldcat.org/issn/1435-9871)

24\. Caswell, Hal (2006). [*Matrix* *population* *models:*
*construction,* *analysis,* *and* *interpretation*
(htt](https://global.oup.com/academic/product/matrix-population-models-9780878931217)
[ps://global.oup.com/academic/product/matrix-population-models-9780878931217)](https://global.oup.com/academic/product/matrix-population-models-9780878931217)

> (Second ed.). Sunderland, Massachusetts: Sinauer Associates, Inc.
> Publishers. [ISBN](https://en.wikipedia.org/wiki/ISBN_(identifier))
> [978-0-87893-121-7.](https://en.wikipedia.org/wiki/Special:BookSources/978-0-87893-121-7)

25\. Bertsimas, D.; Nakazato, D. (1995). ["The Distributional Little's
Law and Its Applications"
(htt](http://web.mit.edu/dbertsim/www/papers/Queuing%20Theory/The%20distributional%20Little's%20law%20and%20its%20applications.pdf)
[p://web.mit.edu/dbertsim/www/papers/Queuing%20Theory/The%20distributional%20Littl](http://web.mit.edu/dbertsim/www/papers/Queuing%20Theory/The%20distributional%20Little's%20law%20and%20its%20applications.pdf)
[e's%20law%20and%20its%20applications.pdf)](http://web.mit.edu/dbertsim/www/papers/Queuing%20Theory/The%20distributional%20Little's%20law%20and%20its%20applications.pdf)
(PDF). [*Operations*
*Research*](https://en.wikipedia.org/wiki/Operations_Research_(journal)).
**43** (2): 298.
[doi](https://en.wikipedia.org/wiki/Doi_(identifier))[:10.1287/opre.43.2.298
(https://doi.org/10.1287%2Fopre.43.2.298)](https://doi.org/10.1287%2Fopre.43.2.298).
[JSTOR](https://en.wikipedia.org/wiki/JSTOR_(identifier)) [171838
(http](https://www.jstor.org/stable/171838)
[s://www.jstor.org/stable/171838)](https://www.jstor.org/stable/171838).

> **External** **links**
>
> [*A* *Proof* *of* *the* *Queueing* *Formula* *L* *=* *λ* *W*
> *(http://www.columbia.edu/~ks20/stochastic-I/stoch*](http://www.columbia.edu/~ks20/stochastic-I/stochastic-I-LL.pdf)
> [*astic-I-LL.pdf)*](http://www.columbia.edu/~ks20/stochastic-I/stochastic-I-LL.pdf),
> Sigman, K., Columbia University
>
> [*A* *Proof* *of* *the* *Queueing* *Formula* *L* *=* *λ* *W*
> *(http://www.columbia.edu/~ks20/stochastic-I/stoch*](http://www.columbia.edu/~ks20/stochastic-I/stochastic-I-LL.pdf)
> [*astic-I-LL.pdf)*](http://www.columbia.edu/~ks20/stochastic-I/stochastic-I-LL.pdf),
> Eduardo, Maldonado., Alexby usm
>
> Retrieved from
> "[https://en.wikipedia.org/w/index.php?title=Little%27s_law&oldid=1308040759"](https://en.wikipedia.org/w/index.php?title=Little%27s_law&oldid=1308040759)
