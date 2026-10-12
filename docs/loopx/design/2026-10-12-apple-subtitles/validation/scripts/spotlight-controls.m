#import <Foundation/Foundation.h>
#import <CoreSpotlight/CoreSpotlight.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
static BOOL waitDone(dispatch_semaphore_t sem,double seconds){NSDate *deadline=[NSDate dateWithTimeIntervalSinceNow:seconds];while(dispatch_semaphore_wait(sem,DISPATCH_TIME_NOW)!=0){if(deadline.timeIntervalSinceNow<=0)return NO;[[NSRunLoop currentRunLoop]runUntilDate:[NSDate dateWithTimeIntervalSinceNow:.02]];}return YES;}
static NSArray *search(NSString *text,BOOL user,BOOL images){
 CSUserQueryContext *context=[CSUserQueryContext userQueryContext];context.enableRankedResults=YES;context.disableSemanticSearch=NO;context.maxResultCount=100;context.maxRankedResultCount=100;context.maxSuggestionCount=0;context.fetchAttributes=@[@"title",@"contentType"];
 if(images)context.filterQueries=@[@"contentTypeTree=\"public.image\""];
 CSSearchQuery *q=user?[[CSUserQuery alloc]initWithUserQueryString:text userQueryContext:context]:[[CSSearchQuery alloc]initWithQueryString:text queryContext:context];
 NSMutableArray *found=[NSMutableArray array];dispatch_semaphore_t sem=dispatch_semaphore_create(0);__block NSError *error=nil;
 q.foundItemsHandler=^(NSArray *items){[found addObjectsFromArray:items];};q.completionHandler=^(NSError *e){error=e;dispatch_semaphore_signal(sem);};[q start];
 if(!waitDone(sem,15)){[q cancel];return @[@"query_timeout"];}
 if(error)return @[[NSString stringWithFormat:@"error:%@",error]];
 [found sortUsingComparator:^NSComparisonResult(CSSearchableItem *a,CSSearchableItem*b){return [a compareByRank:b];}];
 return [found valueForKey:@"uniqueIdentifier"];
}
int main(int argc,const char *argv[]){@autoreleasepool{setbuf(stdout,NULL);
 NSString *base=@"/tmp/cineinsight-apple-validation";NSString *domain=@"cineinsight-visual-probe-20261012";
 printf("bundle=%s\n",NSBundle.mainBundle.bundleIdentifier.UTF8String);
 CSSearchableIndex *index=[[CSSearchableIndex alloc]initWithName:domain];
 if(argc>1 && strcmp(argv[1],"cleanup")==0){dispatch_semaphore_t sem=dispatch_semaphore_create(0);[index deleteAllSearchableItemsWithCompletionHandler:^(NSError*e){printf("cleanup=%s\n",e?e.description.UTF8String:"ok");dispatch_semaphore_signal(sem);}];return waitDone(sem,20)?0:4;}
 NSData *manifestData=[NSData dataWithContentsOfFile:[base stringByAppendingPathComponent:@"coco-manifest.json"]];NSArray *manifest=[NSJSONSerialization JSONObjectWithData:manifestData options:0 error:nil];
 NSMutableArray *items=[NSMutableArray array];
 for(NSDictionary *row in manifest){CSSearchableItemAttributeSet *attrs=[[CSSearchableItemAttributeSet alloc]initWithContentType:UTTypeJPEG];attrs.title=[@"ci-image-" stringByAppendingString:row[@"id"]];attrs.contentURL=[NSURL fileURLWithPath:row[@"path"]];attrs.thumbnailURL=attrs.contentURL;[items addObject:[[CSSearchableItem alloc]initWithUniqueIdentifier:row[@"id"] domainIdentifier:domain attributeSet:attrs]];}
 CSSearchableItemAttributeSet *control=[[CSSearchableItemAttributeSet alloc]initWithContentType:UTTypePlainText];control.title=@"ci-validation-control";control.textContent=@"A dog is playing with a ball in the park.";[items addObject:[[CSSearchableItem alloc]initWithUniqueIdentifier:@"control" domainIdentifier:domain attributeSet:control]];
 dispatch_semaphore_t sem=dispatch_semaphore_create(0);__block NSError *indexError=nil;[index indexSearchableItems:items completionHandler:^(NSError*e){indexError=e;dispatch_semaphore_signal(sem);}];
 if(!waitDone(sem,20)||indexError){printf("index_failed=%s\n",indexError?indexError.description.UTF8String:"timeout");return 3;}
 puts("indexed_metadata=101");[CSUserQuery prepare];
 for(int round=0;round<2;round++){
  if(round){NSDate *until=[NSDate dateWithTimeIntervalSinceNow:20];while(until.timeIntervalSinceNow>0)[[NSRunLoop currentRunLoop]runUntilDate:[NSDate dateWithTimeIntervalSinceNow:.05]];}
  NSArray *literal=search(@"title == \"ci-validation-control\"",NO,NO);
  NSArray *semantic=search(@"puppy fetching toy outdoors",YES,NO);
  NSDictionary *probe=@{@"round":@(round),@"literal_control":literal,@"user_literal_control":search(@"ci-validation-control",YES,NO),@"user_word_control":search(@"dog",YES,NO),@"semantic_control":semantic,@"hydrant":search(@"fire hydrant in grass",YES,YES),@"zh_hydrant":search(@"草地上的消防栓",YES,YES),@"skateboard":search(@"man riding skateboard",YES,YES)};
  NSData *out=[NSJSONSerialization dataWithJSONObject:probe options:0 error:nil];fwrite(out.bytes,1,out.length,stdout);puts("");
 }
 dispatch_semaphore_t cleanup=dispatch_semaphore_create(0);[index deleteAllSearchableItemsWithCompletionHandler:^(NSError*e){printf("cleanup=%s\n",e?e.description.UTF8String:"ok");dispatch_semaphore_signal(cleanup);}];waitDone(cleanup,20);
}}
